#!/usr/bin/env python3
"""T/B/M bench for MiniVectorDB builds, modelled on the task statement 3.3:
write phase = empty table, <batches> multi-row INSERTs of <batch> rows, each
autocommitted; query phase = same load (untimed), one warm-up query, then
<queries> timed TopK queries (K=10, every other one with category = ?).

Targets:
  name=bin:/path/to/binary      native process (T only; B/M not available)
  name=docker:image             docker run with the judge's limits; B and M
                                come from the container cgroup (io.stat,
                                memory.peak), read through the helper
                                container mvdb-cghelper (host cgroup ns).
Every query result is checked against a float64 brute force over the stored
float32 values (ids in order, distances within the judge tolerance).
"""
import argparse, json, os, shutil, subprocess, sys, tempfile, time, uuid
import numpy as np

HELPER = "mvdb-cghelper"


def gen(seed, rows, dim, nq, cats, fmt, schema, batch):
    rng = np.random.default_rng(seed)
    if fmt == "lab":      # float32 values printed with repr(float(x)), no spaces
        store = rng.random((rows, dim), dtype=np.float32)
        qv = rng.random((nq, dim), dtype=np.float32)
        txt = lambda row: ",".join(repr(float(x)) for x in row)
    elif fmt == "json":   # float64 values, json.dumps style separators
        raw = rng.random((rows, dim)); store = raw.astype(np.float32)
        qraw = rng.random((nq, dim)); qv = qraw.astype(np.float32)
        txt = lambda row: ", ".join(repr(float(x)) for x in row)
    elif fmt == "short":  # six decimals
        raw = np.round(rng.random((rows, dim)), 6); store = raw.astype(np.float32)
        qraw = np.round(rng.random((nq, dim)), 6); qv = qraw.astype(np.float32)
        txt = lambda row: ",".join("%.6f" % x for x in row)
    else:
        raise SystemExit("unknown fmt")
    src = store if fmt == "lab" else raw
    qsrc = qv if fmt == "lab" else qraw
    cat = rng.integers(0, cats, rows)
    semi = ";" if schema in ("official", "statement") else ""
    if schema == "statement":
        create = "CREATE TABLE docs (\n    id BIGINT PRIMARY KEY,\n    category VARCHAR(32),\n    content VARCHAR(1024),\n    embedding VECTOR(%d)\n);" % dim
        rowsql = lambda i: "(%d, 'c%d', '文档%d', '[%s]')" % (i + 1, cat[i], i + 1, txt(src[i]))
        sep = ",\n    "
    elif schema in ("official_cn", "official_nl"):
        create = "CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(8), content VARCHAR(16), embedding VECTOR(%d));" % dim
        if schema == "official_cn":
            rowsql = lambda i: "(%d,'c%d','文档%d','[%s]')" % (i + 1, cat[i], i + 1, txt(src[i]))
            sep = ", "
        else:
            rowsql = lambda i: "(%d,'c%d','doc%d','[%s]')" % (i + 1, cat[i], i + 1, txt(src[i]))
            sep = ",\n"
        semi = ";"
    elif schema == "official":
        create = "CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(8), content VARCHAR(16), embedding VECTOR(%d));" % dim
        rowsql = lambda i: "(%d,'c%d','doc%d','[%s]')" % (i + 1, cat[i], i + 1, txt(src[i]))
        sep = ", "
    else:
        create = "CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(16), embedding VECTOR(%d))" % dim
        rowsql = lambda i: "(%d,'c%d','[%s]')" % (i + 1, cat[i], txt(src[i]))
        sep = ","
    batches = []
    for s in range(0, rows, batch):
        head = "INSERT INTO docs VALUES\n    " if schema == "statement" else "INSERT INTO docs VALUES "
        batches.append(head + sep.join(rowsql(i) for i in range(s, min(rows, s + batch))) + semi)
    qs = []
    for j in range(nq):
        c = int(rng.integers(0, cats)) if j % 2 else None
        where = " WHERE category = 'c%d'" % c if c is not None else ""
        if schema == "statement":
            qs.append((c, "SELECT id,\n       L2_DISTANCE(embedding, '[%s]') AS distance\nFROM docs%s\nORDER BY distance\nLIMIT 10;" % (txt(qsrc[j]), "\nWHERE category = 'c%d'" % c if c is not None else "")))
            continue
        qs.append((c, "SELECT id, L2_DISTANCE(embedding, '[%s]') AS distance FROM docs%s ORDER BY distance LIMIT 10%s" % (txt(qsrc[j]), where, semi)))
    return create, batches, qs, store, qv, cat


def truth(store, qv, cat, qs, k=10):
    v64 = store.astype(np.float64)
    ids_all = np.arange(1, len(store) + 1)
    out = []
    for j, (c, _) in enumerate(qs):
        d = ((v64 - qv[j].astype(np.float64)) ** 2).sum(axis=1)
        ids = ids_all
        if c is not None:
            m = cat == c
            d, ids = d[m], ids[m]
        o = np.lexsort((ids, d))[:k]
        out.append((ids[o].tolist(), np.sqrt(d[o]).tolist()))
    return out


def enc(i, sql=None, op="execute"):
    r = {"id": i, "op": op}
    if sql is not None:
        r.update(sql=sql, search_mode="exact")
    return (json.dumps(r, ensure_ascii=False) + "\n").encode()


class DB:
    def __init__(self, kind, target, env, cpus):
        self.kind, self.name, self.vol, self.dir, self.cg = kind, None, None, None, None
        if kind == "docker":
            self.name = "mvb-" + uuid.uuid4().hex[:12]
            self.vol = self.name + "-data"
            subprocess.run(["docker", "volume", "create", self.vol], check=True, capture_output=True)
            argv = ["docker", "run", "-i", "--name", self.name, "--network=none", "--cpus=" + cpus, "--memory=2g",
                    "--memory-swap=2g", "--pids-limit=128", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                    "--read-only", "--mount", "type=volume,src=%s,dst=/data" % self.vol]
            for kv in env:
                argv += ["-e", kv]
            argv.append(target)
            penv = None
        else:
            self.dir = tempfile.mkdtemp(prefix="mvb-")
            argv = [target, "--data-dir", self.dir]
            penv = dict(os.environ)
            for kv in env:
                k, v = kv.split("=", 1); penv[k] = v
        self.argv = argv
        self.penv = penv
        self.start()

    def start(self):
        self.p = subprocess.Popen(self.argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=self.penv, bufsize=0)
        self.rd = os.fdopen(os.dup(self.p.stdout.fileno()), "rb", buffering=1 << 20)
        line = self.rd.readline()
        if json.loads(line) != {"type": "ready"}:
            raise RuntimeError("no ready: %r %r" % (line, self.p.stderr.read()[-2000:]))
        self.seq = 0
        if self.kind == "docker":
            cid = subprocess.check_output(["docker", "inspect", "-f", "{{.Id}}", self.name], text=True).strip()
            self.cg = "/hostcg/docker/" + cid

    def raw(self, b):
        self.p.stdin.write(b)
        return self.rd.readline()

    def req(self, sql=None, op="execute"):
        self.seq += 1
        r = json.loads(self.raw(enc(self.seq, sql, op)))
        if r.get("id") != self.seq or r.get("ok") is not True:
            raise RuntimeError(str(r)[:400])
        return r

    def stats(self):
        if self.kind != "docker":
            return {}
        sh = "cd %s && cat memory.peak && echo ==== && cat memory.current && echo ==== && cat io.stat && echo ==== && cat memory.stat" % self.cg
        o = subprocess.check_output(["docker", "exec", HELPER, "sh", "-c", sh], text=True).split("====\n")
        rb = wb = 0
        for line in o[2].splitlines():
            for kv in line.split()[1:]:
                k, v = kv.split("=")
                if k == "rbytes": rb += int(v)
                elif k == "wbytes": wb += int(v)
        ms = {}
        for line in o[3].splitlines():
            k, v = line.split()
            if k in ("anon", "file", "file_mapped", "kernel", "kernel_stack", "pagetables", "slab", "shmem", "sock", "file_dirty"):
                ms[k] = round(int(v) / 2**20, 3)
        return {"M": int(o[0]) / 2**20, "cur": int(o[1]) / 2**20, "B": (rb + wb) / 2**20, "rb": rb / 2**20, "wb": wb / 2**20, "ms": ms}

    def shutdown(self):
        self.req(op="shutdown")
        self.p.stdin.close()
        err = self.p.stderr.read()
        rc = self.p.wait()
        self.rd.close()
        if rc != 0:
            raise RuntimeError("exit %d: %s" % (rc, err[-2000:]))
        if self.kind == "docker":
            subprocess.run(["docker", "rm", "-f", self.name], capture_output=True)
        return err

    def destroy(self):
        if self.kind == "docker":
            subprocess.run(["docker", "rm", "-f", self.name], capture_output=True)
            subprocess.run(["docker", "volume", "rm", "-f", self.vol], capture_output=True)
        else:
            shutil.rmtree(self.dir, ignore_errors=True)


def run_write(kind, target, env, cpus, create, batches, rows):
    db = DB(kind, target, env, cpus)
    try:
        db.req(create)
        reqs = [enc(i + 2, b) for i, b in enumerate(batches)]
        s0 = db.stats()
        t0 = time.perf_counter()
        resp = [db.raw(r) for r in reqs]
        t = time.perf_counter() - t0
        s1 = db.stats()
        db.seq += len(reqs)
        for r in resp:
            j = json.loads(r)
            assert j.get("ok") is True and j.get("affected_rows") is not None, r[:300]
        err = db.shutdown()
        db.start()          # persistence check, not part of T/B/M
        n = len(db.req("SELECT id FROM docs")["rows"])
        db.shutdown()
        assert n == rows, "restart rows %d != %d" % (n, rows)
        return {"T": t, "pre": s0, **s1, "stderr": diag(err)}
    finally:
        db.destroy()


def run_query(kind, target, env, cpus, create, batches, qs, ref):
    db = DB(kind, target, env, cpus)
    try:
        db.req(create)
        for b in batches:
            db.req(b)
        s_load = db.stats()
        db.req(qs[0][1])    # warm-up
        s_pre = db.stats()
        reqs = [enc(db.seq + 1 + i, q[1]) for i, q in enumerate(qs)]
        t0 = time.perf_counter()
        resp = [db.raw(r) for r in reqs]
        t = time.perf_counter() - t0
        s1 = db.stats()
        db.seq += len(reqs)
        err = db.shutdown()
    finally:
        db.destroy()
    bad_ids = bad_dist = 0
    worst = 0.0
    for r, (ids, ds) in zip(resp, ref):
        j = json.loads(r)
        assert j.get("ok") is True, r[:300]
        got = [row[0] for row in j["rows"]]
        if got != ids:
            bad_ids += 1
            continue
        for row, d in zip(j["rows"], ds):
            e = abs(row[1] - d)
            worst = max(worst, e / max(d, 1e-300))
            if e > max(1e-5, 1e-5 * max(abs(d), abs(row[1]))):
                bad_dist += 1
    return {"T": t, "load": s_load, "pre": s_pre, **s1, "bad_ids": bad_ids, "bad_dist": bad_dist, "worst_rel": worst, "stderr": diag(err)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--target", action="append", required=True, help="name=bin:PATH or name=docker:IMAGE")
    ap.add_argument("--rounds", type=int, default=3)
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--rows", type=int, default=10000)
    ap.add_argument("--dim", type=int, default=128)
    ap.add_argument("--queries", type=int, default=1000)
    ap.add_argument("--batch", type=int, default=100)
    ap.add_argument("--cats", type=int, default=10)
    ap.add_argument("--fmt", default="lab")
    ap.add_argument("--schema", default="lab")
    ap.add_argument("--env", action="append", default=[])
    ap.add_argument("--cpus", default="1")
    ap.add_argument("--phases", default="write,query")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    create, batches, qs, store, qv, cat = gen(a.seed, a.rows, a.dim, a.queries, a.cats, a.fmt, a.schema, a.batch)
    ref = truth(store, qv, cat, qs)
    targets = []
    for t in a.target:
        name, rest = t.split("=", 1)
        kind, tgt = rest.split(":", 1)
        targets.append((name, kind, tgt))
    with open(a.out, "a") as f:
        for r in range(a.rounds):
            order = targets if r % 2 == 0 else targets[::-1]
            for name, kind, tgt in order:
                rec = {"name": name, "round": r, "fmt": a.fmt, "schema": a.schema, "rows": a.rows, "dim": a.dim, "queries": a.queries}
                if "write" in a.phases:
                    rec["write"] = run_write(kind, tgt, a.env, a.cpus, create, batches, a.rows)
                if "query" in a.phases:
                    rec["query"] = run_query(kind, tgt, a.env, a.cpus, create, batches, qs, ref)
                f.write(json.dumps(rec) + "\n"); f.flush()
                w, q = rec.get("write", {}), rec.get("query", {})
                print("%-14s r%d  W: T=%.4f B=%s M=%s | Q: T=%.4f B=%s M=%s bad=%s/%s" % (
                    name, r, w.get("T", 0), fmtn(w.get("B")), fmtn(w.get("M")), q.get("T", 0), fmtn(q.get("B")), fmtn(q.get("M")),
                    q.get("bad_ids"), q.get("bad_dist")), flush=True)


def diag(err):
    return [l for l in err.decode("utf-8", "replace").splitlines() if any(k in l for k in ("memstats", "smaps_rollup", "status ", "syncstats", "leaddiag", "gc "))]


def fmtn(x):
    return "-" if x is None else "%.3f" % x


if __name__ == "__main__":
    main()
