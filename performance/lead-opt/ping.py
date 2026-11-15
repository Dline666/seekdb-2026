#!/usr/bin/env python3
"""Round-trip floor: N x `SELECT 1` against a native binary over pipes."""
import json, os, subprocess, sys, tempfile, time, shutil
binp, n = sys.argv[1], int(sys.argv[2])
extra_env = dict(kv.split("=", 1) for kv in sys.argv[3:])
d = tempfile.mkdtemp(prefix="ping-")
p = subprocess.Popen([binp, "--data-dir", d], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0, env=dict(os.environ, **extra_env))
rd = os.fdopen(os.dup(p.stdout.fileno()), "rb", buffering=1 << 16)
assert json.loads(rd.readline()) == {"type": "ready"}
reqs = [(json.dumps({"id": i + 1, "op": "execute", "sql": "SELECT 1", "search_mode": "exact"}) + "\n").encode() for i in range(n)]
for r in reqs[:200]:
    p.stdin.write(r); rd.readline()
lat = []
t0 = time.perf_counter()
for r in reqs:
    a = time.perf_counter()
    p.stdin.write(r); rd.readline()
    lat.append(time.perf_counter() - a)
tot = time.perf_counter() - t0
lat.sort()
print("ping n=%d total=%.4fs mean=%.1fus p50=%.1fus p90=%.1fus p99=%.1fus" % (n, tot, tot / n * 1e6, lat[n // 2] * 1e6, lat[n * 9 // 10] * 1e6, lat[n * 99 // 100] * 1e6))
p.stdin.write((json.dumps({"id": 0, "op": "shutdown"}) + "\n").encode()); rd.readline(); p.wait()
shutil.rmtree(d, ignore_errors=True)
