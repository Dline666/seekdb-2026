#!/usr/bin/env python3
"""Transport floor of the write workload: 100 requests of the batch size that
the engine only has to read, JSON-decode and skip as a comment."""
import json, os, subprocess, sys, tempfile, time, shutil
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import bench
binp = sys.argv[1]
env = dict(os.environ, **dict(kv.split("=", 1) for kv in sys.argv[2:]))
create, batches, qs, store, qv, cat = bench.gen(7, 10000, 128, 10, 10, "lab", "official", 100)
d = tempfile.mkdtemp(prefix="floor-")
p = subprocess.Popen([binp, "--data-dir", d], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0, env=env)
rd = os.fdopen(os.dup(p.stdout.fileno()), "rb", buffering=1 << 16)
assert json.loads(rd.readline()) == {"type": "ready"}
reqs = [bench.enc(i + 1, "SELECT 1 /*" + b.replace("*/", "") + "*/") for i, b in enumerate(batches)]
res = []
for rep in range(5):
    t0 = time.perf_counter()
    for r in reqs:
        p.stdin.write(r); rd.readline()
    res.append(time.perf_counter() - t0)
print("floor(samesize x100): " + " ".join("%.4f" % x for x in res), "bytes=%d" % sum(len(r) for r in reqs))
p.stdin.write(bench.enc(0, None, "shutdown")); rd.readline(); p.wait(); shutil.rmtree(d, ignore_errors=True)
