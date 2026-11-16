#!/usr/bin/env python3
"""Protocol-level differential fuzzer for two MiniVectorDB binaries.

Both binaries get the identical stream of JSONL requests (each on its own
data directory) and every response line must be byte-identical. The stream
mixes DDL, DML, point/range/TopK queries, transactions, error cases, protocol
errors, normal restarts and kill -9 between requests (never mid-request, so
the outcome is deterministic). Vector components come from a small set so
distance ties are common and the primary-key tie-break is exercised.

usage: diff_fuzz.py REF_BIN CAND_BIN --seeds A-B [--steps N] [--cand-env K=V ...]
"""
import argparse, json, os, random, shutil, signal, subprocess, sys, tempfile, time


class Proc:
    def __init__(self, binp, env, chunk_rng=None):
        self.binp, self.env = binp, env
        self.chunk_rng = chunk_rng
        self.dir = tempfile.mkdtemp(prefix="dfz-")
        self.start()

    def start(self):
        self.p = subprocess.Popen([self.binp, "--data-dir", self.dir], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.DEVNULL, bufsize=0, env=self.env)
        self.rd = os.fdopen(os.dup(self.p.stdout.fileno()), "rb", buffering=1 << 20)
        line = self.rd.readline()
        if line != b'{"type":"ready"}\n':
            raise RuntimeError("no ready: %r" % line)

    def send(self, raw):
        r = self.chunk_rng
        if r is None or len(raw) < 64 or r.random() < 0.3:
            self.p.stdin.write(raw)
            return self.rd.readline()
        # Deliver the line in pieces, like a pipe chain does.
        i, n = 0, len(raw)
        while i < n:
            step = r.choice([1, 7, 100, 4096, 30000, 65536, n]) if r.random() < 0.5 else r.randrange(1, max(2, n // 3))
            self.p.stdin.write(raw[i:i + step])
            i += step
            if r.random() < 0.3:
                time.sleep(r.choice([0, 0.00005, 0.0002, 0.001]))
        return self.rd.readline()

    def shutdown(self):
        r = self.send(b'{"id":0,"op":"shutdown"}\n')
        self.p.stdin.close()
        rest = self.rd.read()
        rc = self.p.wait()
        self.rd.close()
        return r + rest + b"|rc=%d" % rc

    def kill(self):
        self.p.send_signal(signal.SIGKILL)
        self.p.wait()
        self.p.stdin.close()
        self.rd.close()

    def destroy(self):
        try:
            if self.p.poll() is None:
                self.p.kill(); self.p.wait()
        finally:
            shutil.rmtree(self.dir, ignore_errors=True)


VALS = ["0", "1", "2", "-1", "0.5", "1.5", "3", "0.25", "-0.5", "4"]
WORDS = ["a", "b", "c", "keep", "skip", "x", "甲", "乙", "丙", "", "it''s", "a\\\\b", "q\"q", "long-ish-text", "zz"]


class Gen:
    def __init__(self, rng):
        self.r = rng
        self.tables = {}   # name -> dict(cols=[(name,type,param)], dim, next_id)
        self.dropped = []
        self.in_txn = False

    def vec(self, dim, fancy=True):
        r = self.r
        k = r.random()
        if fancy and k < 0.03:
            dim = max(0, dim + r.choice([-1, 1]))
        comps = []
        for _ in range(dim):
            x = r.random()
            if x < 0.75:
                comps.append(r.choice(VALS))
            elif x < 0.9:
                comps.append(repr(round(r.uniform(-3, 3), r.choice([1, 3, 6, 17]))))
            elif x < 0.95:
                comps.append(r.choice(["1e2", "2.5E-1", "+1", ".5", "5.", "1e-3", "0.1", "16777217", "3.4e38", "1e-46"]))
            elif fancy and x < 0.97:
                comps.append(r.choice(["nan", "inf", "1e39", "-1e39", "abc", "", "1e", "0x10", "1_0", "NaN", "Infinity"]))
            else:
                comps.append(r.choice(VALS))
        sep = r.choice([",", ",", ", ", " , "])
        s = "[" + sep.join(comps) + "]"
        if fancy and r.random() < 0.02:
            s = r.choice(["[", "]", "1,2", "[1,2", "[1,,2]", "[1,2,]", " [ 1 , 2 ] ", "[]", "'"])
        return s

    def create(self):
        r = self.r
        name = "t%d" % r.randrange(4)
        cols = [("id", "BIGINT", 0)]
        if r.random() < 0.8:
            cols.append(("category", "VARCHAR", r.choice([1, 4, 8, 16])))
        if r.random() < 0.5:
            cols.append(("content", "VARCHAR", r.choice([2, 8, 16])))
        if r.random() < 0.5:
            cols.append(("n", "BIGINT", 0))
        dim = 0
        if r.random() < 0.85:
            dim = r.choice([1, 2, 3, 8, 16, 17, 32])
            cols.insert(r.randrange(1, len(cols) + 1), ("embedding", "VECTOR", dim))
        if r.random() < 0.15:
            r.shuffle(cols)
        defs = []
        for c, t, p in cols:
            d = c + " " + t + ("(%d)" % p if t != "BIGINT" else "")
            if c == "id" and r.random() < 0.97:
                d += " PRIMARY KEY"
            defs.append(d)
        ine = "IF NOT EXISTS " if r.random() < 0.2 else ""
        sql = "CREATE TABLE %s%s (%s)" % (ine, name, ", ".join(defs))
        if name not in self.tables and "PRIMARY KEY" in sql:
            self.tables[name] = dict(cols=cols, dim=dim, next_id=1)
        return sql

    def table(self):
        r = self.r
        if self.tables and r.random() < 0.97:
            return r.choice(sorted(self.tables))
        return "t%d" % r.randrange(5)

    def value(self, t, col, typ, param, clean=False):
        r = self.r
        if clean:
            if typ == "BIGINT":
                if col == "id":
                    T = self.tables[t]
                    T["next_id"] += 1
                    return str(T["next_id"] - 1)
                return str(r.randrange(-3, 12))
            if typ == "VARCHAR":
                w = r.choice(WORDS)
                n = len(w.replace("''", "'").replace("\\\\", "\\"))
                return "'" + (w if n <= param else "") + "'"
            return "'" + self.vec(param, False) + "'"
        if typ == "BIGINT":
            if col == "id":
                T = self.tables.get(t)
                if T and r.random() < 0.8:
                    T["next_id"] += 1
                    return str(T["next_id"] - 1)
                return str(r.choice([1, 2, 3, 5, 8, 13, 9007199254740993, -4, 0, r.randrange(1, 40)]))
            x = r.random()
            if x < 0.85:
                return str(r.randrange(-3, 12))
            return r.choice(["9223372036854775807", "-9223372036854775808", "9223372036854775808", "1.5", "'7'", "'x'", "1e3"])
        if typ == "VARCHAR":
            w = r.choice(WORDS)
            if r.random() < 0.05:
                w = w * 6
            if r.random() < 0.03:
                return str(r.randrange(100))
            return "'" + w + "'"
        return "'" + self.vec(param) + "'"

    def insert(self):
        r = self.r
        t = self.table()
        T = self.tables.get(t)
        cols = T["cols"] if T else [("id", "BIGINT", 0), ("category", "VARCHAR", 8)]
        order = list(cols)
        collist = ""
        if r.random() < 0.3:
            r.shuffle(order)
            if r.random() < 0.1 and len(order) > 1:
                order = order[:-1]
            names = [c for c, _, _ in order]
            if r.random() < 0.05:
                names.append(r.choice(["nope", names[0]]))
            collist = " (" + ", ".join(names) + ")"
        nrows = r.choice([1, 1, 1, 2, 3, 5, 8, 40]) if r.random() < 0.95 else r.choice([300, 700, 1500])
        clean = T is not None and collist == "" and (r.random() < 0.7 or nrows >= 300 and r.random() < 0.8)
        rows = []
        for _ in range(nrows):
            vals = [self.value(t, c, ty, p, clean) for c, ty, p in order]
            x = 1.0 if clean else r.random()
            if x < 0.02:
                vals = vals[:-1]
            elif x < 0.04:
                vals.append("1")
            elif x < 0.05:
                vals[r.randrange(len(vals))] = r.choice(["NULL", "1+1", "-(2)", "L2_DISTANCE('[1]','[2]')", "id"])
            rows.append("(" + r.choice([", ", ","]).join(vals) + ")")
        sep = r.choice([",", ", ", " ,\n "])
        tail = r.choice(["", "", ";", " ON DUPLICATE KEY UPDATE id=id"]) if r.random() < 0.3 and not clean else ""
        kw = r.choice(["INSERT INTO", "insert into", "INSERT INTO", "INSERT IGNORE INTO"]) if r.random() < 0.1 and not clean else "INSERT INTO"
        return "%s %s%s VALUES %s%s" % (kw, t, collist, sep.join(rows), tail)

    def cond(self, T):
        r = self.r
        parts = []
        for _ in range(r.choice([1, 1, 1, 2, 3])):
            c, ty, p = r.choice(T["cols"]) if T else ("id", "BIGINT", 0)
            op = r.choice(["=", "=", "=", "<", "<=", ">", ">=", "<>"]) if r.random() < 0.97 else r.choice(["!=", "LIKE", "IS", "IN"])
            if ty == "BIGINT":
                v = str(r.randrange(-2, 30)) if r.random() < 0.9 else r.choice(["'3'", "2.5", "'x'", "9007199254740993"])
            elif ty == "VARCHAR":
                v = "'" + r.choice(WORDS) + "'" if r.random() < 0.95 else str(r.randrange(5))
            else:
                v = "'" + self.vec(p, False) + "'"
            parts.append("%s %s %s" % (c, op, v) if r.random() < 0.9 else "%s %s %s" % (v, op, c))
        return (" AND " if r.random() < 0.97 else " OR ").join(parts)

    def select(self):
        r = self.r
        t = self.table()
        T = self.tables.get(t)
        cols = [c for c, _, _ in T["cols"]] if T else ["id"]
        x = r.random()
        if T and T["dim"] and x < 0.5:
            q = self.vec(T["dim"], r.random() < 0.3)
            proj = r.choice(["id, L2_DISTANCE(embedding, '%s') AS distance" % q, "id, L2_DISTANCE(embedding, '%s') AS d, category" % q if "category" in cols else "id",
                             "*, L2_DISTANCE(embedding, '%s') AS distance" % q, "L2_DISTANCE(embedding, '%s') AS distance, id" % q, "id", "embedding, id"])
            where = " WHERE " + self.cond(T) if r.random() < 0.5 else ""
            if "distance" in proj and r.random() < 0.8:
                order = "distance"
            elif " AS d" in proj and r.random() < 0.8:
                order = "d"
            else:
                order = "L2_DISTANCE(embedding, '%s')" % q
            order += r.choice(["", "", "", " ASC", " DESC", " APPROXIMATE", ", id", ", id DESC"])
            lim = " LIMIT %d" % r.choice([1, 2, 3, 5, 10, 10, 100, 0])
            if r.random() < 0.1:
                lim += " OFFSET %d" % r.randrange(4)
            if r.random() < 0.03:
                lim = ""
            return "SELECT %s FROM %s%s ORDER BY %s%s" % (proj, t, where, order, lim) + (";" if r.random() < 0.3 else "")
        proj = r.choice(["*", "id", ", ".join(r.sample(cols, r.randrange(1, len(cols) + 1))), "id AS k, " + cols[-1], "COUNT(*)", "nope", "id, id", "1", "1+1"])
        sql = "SELECT %s FROM %s" % (proj, t)
        if r.random() < 0.6:
            sql += " WHERE " + self.cond(T)
        if r.random() < 0.7:
            oc = r.choice(cols + ["1", "k", "id"])
            sql += " ORDER BY %s%s" % (oc, r.choice(["", " DESC", " ASC", ", id", ", id DESC"]))
        else:
            sql += " ORDER BY id"      # unordered results are not comparable byte for byte
        if r.random() < 0.3:
            sql += " LIMIT %d" % r.randrange(0, 6)
            if r.random() < 0.3:
                sql += " OFFSET %d" % r.randrange(3)
        return sql

    def update(self):
        r = self.r
        t = self.table()
        T = self.tables.get(t)
        cols = T["cols"] if T else [("id", "BIGINT", 0)]
        sets = []
        for c, ty, p in r.sample(cols, r.randrange(1, min(3, len(cols)) + 1)):
            if c == "id" and r.random() < 0.9:
                continue
            sets.append("%s = %s" % (c, self.value(t, "x" + c if ty == "BIGINT" else c, ty, p)))
        if not sets:
            sets = ["id = id"] if r.random() < 0.3 else ["%s = %s" % (cols[-1][0], self.value(t, "x", cols[-1][1], cols[-1][2]))]
        sql = "UPDATE %s SET %s" % (t, ", ".join(sets))
        if r.random() < 0.9:
            sql += " WHERE " + self.cond(T)
        return sql

    def delete(self):
        t = self.table()
        sql = "DELETE FROM %s" % t
        if self.r.random() < 0.92:
            sql += " WHERE " + self.cond(self.tables.get(t))
        return sql

    def misc(self):
        r = self.r
        t = self.table()
        return r.choice([
            "ALTER TABLE %s ADD COLUMN z BIGINT" % t, "SELECT * FROM %s a JOIN %s b" % (t, t), "SELECT id FROM %s WHERE id = 1 OR id = 2" % t,
            "SELECT id FROM %s WHERE id IS NULL" % t, "TRUNCATE TABLE %s" % t, "SHOW TABLES", "", ";", "SELECT", "garbage ((", "SELECT 1", "SELECT 'a'",
            "SELECT L2_DISTANCE('[3,4]', '[0,0]') AS d", "SELECT L2_DISTANCE('[1]', '[1,2]')", "SET search_mode = 'ann'", "SET search_mode = 'exact'",
            "INSERT INTO %s SELECT * FROM %s" % (t, t), "SAVEPOINT s", "DESCRIBE %s" % t, "SELECT id FROM %s ORDER BY embedding" % t,
            "CREATE VECTOR INDEX idx_%s ON %s(category)" % (t, t), "CREATE INDEX i ON %s(id)" % t, "SELECT id FROM %s LIMIT -1" % t, "select ID from %s order by ID" % t.upper(),
        ])

    def step(self):
        r = self.r
        x = r.random()
        if not self.tables or x < 0.04:
            return self.create()
        if x < 0.40:
            return self.insert()
        if x < 0.68:
            return self.select()
        if x < 0.76:
            return self.update()
        if x < 0.81:
            return self.delete()
        if x < 0.86:
            if self.in_txn or r.random() < 0.15:
                self.in_txn = False
                return r.choice(["COMMIT", "COMMIT", "ROLLBACK"])
            self.in_txn = True
            return r.choice(["BEGIN", "START TRANSACTION", "BEGIN"])
        if x < 0.89:
            t = self.table()
            ine = "IF NOT EXISTS " if r.random() < 0.3 else ""
            return "CREATE VECTOR INDEX %sidx_%s ON %s(embedding)" % (ine, t, t)
        if x < 0.90:
            t = self.table()
            if r.random() < 0.5:
                self.tables.pop(t, None)
            else:
                t = "t9"
            self.in_txn = False   # DDL commits; state tracking is best-effort
            return "DROP TABLE %s%s" % ("IF EXISTS " if r.random() < 0.3 else "", t)
        return self.misc()


STATS = {True: 0, False: 0}


def run_seed(seed, ref_bin, cand_bin, steps, ref_env, cand_env, verbose, chunked=False):
    rng = random.Random(seed)
    g = Gen(rng)
    A, B = Proc(ref_bin, ref_env), Proc(cand_bin, cand_env, random.Random(seed * 7919 + 1) if chunked else None)
    log = []
    try:
        ident = 0
        for i in range(steps):
            x = rng.random()
            if x < 0.012:
                ra, rb = A.shutdown(), B.shutdown()
                log.append(("--restart", ra))
                if ra != rb:
                    return i, "--restart", ra, rb, log
                A.start(); B.start(); g.in_txn = False
                continue
            if x < 0.02:
                A.kill(); B.kill(); A.start(); B.start(); g.in_txn = False
                log.append(("--kill9", b""))
                continue
            ident += 1
            if x < 0.03:
                raw = rng.choice([b'{"id":%d,"op":"execute"}\n' % ident, b'{"id":%d,"op":"nope","sql":"SELECT 1"}\n' % ident, b'not json\n',
                                  b'{"id":"s%d","op":"execute","sql":"SELECT 1","search_mode":"exact"}\n' % ident,
                                  b'{"op":"execute","sql":"SELECT 1"}\n', b'{"id":%d,"op":"execute","sql":"SELECT 1","search_mode":"fast"}\n' % ident,
                                  b'{"id":%d,"op":"execute","sql":"SELECT \\"a\\\\nb\\" AS s","search_mode":"exact"}\n' % ident, b'[1,2]\n',
                                  b'{"id":%d.5,"op":"execute","sql":"SELECT 1"}\n' % ident, b'{"id":%d,"op":"execute","sql":"SELECT 1","extra":{"a":[1]}}\n' % ident])
            else:
                sql = g.step()
                mode = "exact" if rng.random() < 0.8 else "ann"
                req = {"id": ident, "op": "execute", "sql": sql, "search_mode": mode}
                if rng.random() < 0.03:
                    del req["search_mode"]
                raw = (json.dumps(req, ensure_ascii=rng.random() < 0.3) + "\n").encode()
            ra, rb = A.send(raw), B.send(raw)
            STATS[b'"ok":true' in ra] += 1
            log.append((raw, ra))
            if verbose:
                sys.stdout.write("%s\n  -> %s" % (raw.decode()[:300].rstrip(), ra.decode()[:300]))
            if ra != rb:
                return i, raw, ra, rb, log
        ra, rb = A.shutdown(), B.shutdown()
        if ra != rb:
            return steps, "--final-shutdown", ra, rb, log
        return None
    finally:
        A.destroy(); B.destroy()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("ref"); ap.add_argument("cand")
    ap.add_argument("--seeds", default="1-50")
    ap.add_argument("--steps", type=int, default=400)
    ap.add_argument("--cand-env", action="append", default=[])
    ap.add_argument("--ref-env", action="append", default=[])
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--chunked", action="store_true", help="write candidate requests in random pieces")
    a = ap.parse_args()
    lo, hi = (int(x) for x in a.seeds.split("-"))
    base = dict(os.environ, GOMAXPROCS="2")
    ref_env = dict(base, **dict(kv.split("=", 1) for kv in a.ref_env))
    cand_env = dict(base, **dict(kv.split("=", 1) for kv in a.cand_env))
    bad = 0
    stats = {"ok": 0, "err": 0}
    for seed in range(lo, hi + 1):
        res = run_seed(seed, a.ref, a.cand, a.steps, ref_env, cand_env, a.verbose, a.chunked)
        if res is not None:
            bad += 1
            i, raw, ra, rb, log = res
            print("MISMATCH seed=%d step=%d\n  req : %s\n  ref : %s\n  cand: %s" % (seed, i, (raw if isinstance(raw, str) else raw.decode("utf-8", "replace"))[:1500].rstrip(), ra[:1500], rb[:1500]))
            with open("dfz-seed%d.log" % seed, "wb") as f:
                for q, r in log:
                    f.write((q if isinstance(q, bytes) else q.encode()) + b"  => " + r + b"\n")
    print("seeds %d-%d steps=%d mismatches=%d ok_responses=%d error_responses=%d" % (lo, hi, a.steps, bad, STATS[True], STATS[False]))
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
