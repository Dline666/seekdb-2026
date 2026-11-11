#!/usr/bin/env python3
"""Linux/cgroup v2 performance lab for MiniVectorDB submissions.

For each commit: docker build the commit's Dockerfile, then for each round
run two independent containers with the judge's limits:
  write: empty /data, CREATE TABLE, time 100 autocommit INSERTs of 100 rows
  query: one container loads the same rows (untimed), runs one warm-up
         query, then 1000 timed TopK queries (K=10, half with
         category = ?); recall and order are checked against numpy truth.
Each phase reports T (s), B (cgroup io.stat rbytes+wbytes, MiB) and
M (cgroup memory.peak, MiB) of the container that ran it.
"""
import argparse
import json
import os
import random
import struct
import subprocess
import sys
import tempfile
import time
import uuid

import numpy as np

LIMITS = ["--network=none", "--cpus=2", "--memory=2g", "--memory-swap=2g", "--pids-limit=128",
          "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only"]


def f32(x):
    return struct.unpack("f", struct.pack("f", x))[0]


def cg_dir(cid):
    for p in (f"/sys/fs/cgroup/system.slice/docker-{cid}.scope", f"/sys/fs/cgroup/docker/{cid}"):
        if os.path.isdir(p):
            return p
    raise RuntimeError("cgroup dir not found for " + cid)


def cg_stats(d):
    peak = int(open(os.path.join(d, "memory.peak")).read())
    rb = wb = 0
    for line in open(os.path.join(d, "io.stat")):
        for kv in line.split()[1:]:
            k, v = kv.split("=")
            if k == "rbytes":
                rb += int(v)
            elif k == "wbytes":
                wb += int(v)
    return {"mem_peak_mib": peak / 2**20, "io_mib": (rb + wb) / 2**20, "rbytes": rb, "wbytes": wb}


class DB:
    def __init__(self, image, data):
        self.name = "lab-" + uuid.uuid4().hex[:12]
        self.p = subprocess.Popen(["docker", "run", "-i", "--name", self.name, *LIMITS,
                                   "--mount", f"type=bind,src={data},dst=/data", image],
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  text=True, bufsize=1 << 20)
        line = self.p.stdout.readline()
        assert json.loads(line) == {"type": "ready"}, line
        cid = subprocess.check_output(["docker", "inspect", "-f", "{{.Id}}", self.name], text=True).strip()
        self.cg = cg_dir(cid)
        self.seq = 0

    def req(self, sql=None, op="execute"):
        self.seq += 1
        r = {"id": self.seq, "op": op}
        if sql is not None:
            r.update(sql=sql, search_mode="exact")
        self.p.stdin.write(json.dumps(r) + "\n")
        self.p.stdin.flush()
        resp = json.loads(self.p.stdout.readline())
        assert resp.get("id") == self.seq and resp.get("ok") is True, str(resp)[:300]
        return resp

    def stats(self):
        return cg_stats(self.cg)

    def close(self):
        self.req(op="shutdown")
        self.p.stdin.close()
        err = self.p.stderr.read()
        rc = self.p.wait()
        subprocess.run(["docker", "rm", "-f", self.name], capture_output=True)
        assert rc == 0, err[-2000:]
        return err


def workload(seed, rows=10000, dim=128, nq=1000, cats=10):
    rng = random.Random(seed)
    vecs = np.array([[f32(rng.random()) for _ in range(dim)] for _ in range(rows)], dtype=np.float32)
    cat = [rng.randrange(cats) for _ in range(rows)]
    batches = []
    for s in range(0, rows, 100):
        vals = ",".join("(%d,'c%d','[%s]')" % (i + 1, cat[i], ",".join(repr(float(x)) for x in vecs[i]))
                        for i in range(s, s + 100))
        batches.append("INSERT INTO docs VALUES " + vals)
    qs = []
    for j in range(nq):
        q = np.array([f32(rng.random()) for _ in range(dim)], dtype=np.float32)
        c = rng.randrange(cats) if j % 2 else None
        where = " WHERE category = 'c%d'" % c if c is not None else ""
        sql = "SELECT id, L2_DISTANCE(embedding, '[%s]') AS distance FROM docs%s ORDER BY distance LIMIT 10" % (
            ",".join(repr(float(x)) for x in q), where)
        qs.append((q, c, sql))
    return vecs, np.array(cat), batches, qs


def truth(vecs, cat, qs, k=10):
    v64 = vecs.astype(np.float64)
    out = []
    for q, c, _ in qs:
        d = ((v64 - q.astype(np.float64)) ** 2).sum(axis=1)
        ids = np.arange(1, len(vecs) + 1)
        if c is not None:
            m = cat == c
            d, ids = d[m], ids[m]
        o = np.lexsort((ids, d))[:k]
        out.append((ids[o].tolist(), d[o].tolist()))
    return out


def run_write(image, batches):
    with tempfile.TemporaryDirectory(prefix="w-", dir=os.environ.get("LAB_TMP")) as d:
        os.chmod(d, 0o777)
        db = DB(image, d)
        db.req("CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(16), embedding VECTOR(128))")
        s0 = db.stats()
        t0 = time.perf_counter()
        for b in batches:
            assert db.req(b)["affected_rows"] == 100
        t = time.perf_counter() - t0
        s1 = db.stats()
        db.close()
        # Persistence check: restart and count.
        db = DB(image, d)
        n = db.req("SELECT id FROM docs")["rows"]
        db.close()
        return {"T_s": t, "B_mib": s1["io_mib"], "M_mib": s1["mem_peak_mib"], "rbytes": s1["rbytes"],
                "wbytes": s1["wbytes"], "B_during_mib": s1["io_mib"] - s0["io_mib"], "restart_rows": len(n)}


def run_query(image, batches, qs, ref):
    # One container from start to end: load (untimed), warm-up, timed
    # queries. B and M cover the whole run, as in the task statement.
    with tempfile.TemporaryDirectory(prefix="q-", dir=os.environ.get("LAB_TMP")) as d:
        os.chmod(d, 0o777)
        db = DB(image, d)
        db.req("CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(16), embedding VECTOR(128))")
        for b in batches:
            db.req(b)
        db.req(qs[0][2])  # warm-up
        t0 = time.perf_counter()
        res = []
        tpart = {"unf": 0.0, "fil": 0.0}
        for _, c, sql in qs:
            t1 = time.perf_counter()
            res.append(db.req(sql))
            tpart["fil" if c is not None else "unf"] += time.perf_counter() - t1
        t = time.perf_counter() - t0
        s = db.stats()
        # Transport floor (not part of T/B/M): same-size requests that do no
        # table work, a constant L2_DISTANCE between two literal vectors.
        floor_sqls = []
        for _, _, sql in qs:
            lit = sql.split("'[")[1].split("]'")[0]
            floor_sqls.append("SELECT L2_DISTANCE('[%s]', '[%s]') AS d" % (lit, lit))
        t1 = time.perf_counter()
        for f_sql in floor_sqls:
            db.req(f_sql)
        tfloor = time.perf_counter() - t1
        t1 = time.perf_counter()
        for _ in qs:
            db.req("SELECT 1")
        tping = time.perf_counter() - t1
        db.close()
    rec = {"unf": [], "fil": []}
    bad_order = bad_dist = 0
    for (q, c, _), r, (ids, d2) in zip(qs, res, ref):
        got = [row[0] for row in r["rows"]]
        if ids:
            rec["fil" if c is not None else "unf"].append(len(set(ids) & set(got)) / min(10, len(ids)))
        if got != ids:
            bad_order += 1
        for row, x in zip(r["rows"], d2):
            if abs(row[1] - x ** 0.5) > 1e-5 * max(1, x ** 0.5):
                bad_dist += 1
    return {"T_s": t, "B_mib": s["io_mib"], "M_mib": s["mem_peak_mib"], "rbytes": s["rbytes"], "wbytes": s["wbytes"],
            "T_unf_s": tpart["unf"], "T_fil_s": tpart["fil"], "floor_samesize_s": tfloor, "floor_ping_s": tping,
            "recall_unf": sum(rec["unf"]) / len(rec["unf"]), "recall_fil": sum(rec["fil"]) / len(rec["fil"]),
            "order_mismatch": bad_order, "dist_mismatch": bad_dist}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--images", nargs="+", required=True, help="tag=image pairs")
    ap.add_argument("--rounds", type=int, default=3)
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--out", required=True)
    ap.add_argument("--tests", default="", help="file with commands to run before the benchmark")
    a = ap.parse_args()
    vecs, cat, batches, qs = workload(a.seed)
    ref = truth(vecs, cat, qs)
    imgs = [x.split("=", 1) for x in a.images]
    with open(a.out, "a") as f:
        for r in range(a.rounds):
            for tag, img in imgs:  # interleave images inside each round
                w = run_write(img, batches)
                q = run_query(img, batches, qs, ref)
                rec = {"tag": tag, "round": r, "write": w, "query": q}
                print(json.dumps(rec), flush=True)
                f.write(json.dumps(rec) + "\n")


if __name__ == "__main__":
    main()
