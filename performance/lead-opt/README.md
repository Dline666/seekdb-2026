Tools and results for the write-path and request-read changes made on top of
`a268b0cd`. Everything here is local measurement; none of it is an official
score.

Tools (Python 3 + numpy, no other dependencies):

- `bench.py`: the write and query phases of the task statement (3.3) against
  `name=bin:PATH` (native process, T only) or `name=docker:IMAGE` (judge
  limits; B from cgroup `io.stat`, M from `memory.peak`, read through a helper
  container started with
  `docker run -d --name mvdb-cghelper --cgroupns=host -v /sys/fs/cgroup:/hostcg:ro alpine sleep infinity`).
  Every TopK result is checked against a float64 brute force.
- `diff_fuzz.py REF CAND --seeds A-B [--chunked] [--cand-env K=V]`: sends the
  same randomized request stream to two binaries and requires byte-identical
  response lines (DDL, DML, TopK with ties, transactions, protocol errors,
  restarts, kill -9 between requests).
- `ping.py`, `floor.py`: request round trip and same-size transport floor.
- `relay/`: `relay N CMD...` chains N copy processes in front of CMD to
  emulate the hops of `docker run -i`.

Results (reference = the engine of `a268b0cd` plus the CREATE VECTOR INDEX
fix; candidate = that plus the literal parser, the INSERT fast path and stdin
polling; interleaved rounds, medians):

| Setup | Metric | Reference | Candidate |
| --- | --- | --- | --- |
| Linux VM, 2 vCPU Xeon 2.2 GHz (one core, two threads), direct pipes, sync off, 5 rounds | write T | 0.178 s | 0.130 s |
| same, fdatasync on (4 to 5 ms per call on that disk), 4 rounds | write T | 0.70 s | 0.62 s |
| same VM, `relay 3`, sync off, 6 rounds | write T | 1.32 s | 1.15 s |
| same (single rounds 0.33-0.39 s and 0.29-0.44 s) | query T | 0.343 s | 0.300 s |
| direct pipes, request round trip (`ping.py`, p50, 3 pairs) | polling off / on | 23.6 us | 15.3 us |
| macOS x86_64 i7-9750H, direct pipes, sync off, 5 rounds | write T | 0.102 s | 0.056 s |
| Docker on a 1-CPU VM, cgroup v2, btrfs volume, GOMAXPROCS=2, warm rounds | write M | 9.43 MiB | 9.68 MiB |
| same | query M | 11.58 MiB | 11.93 MiB |
| same | B per phase | 6.05 MiB | 6.05 MiB |

Query T without the relay chain is unchanged within noise (about 0.21 s on
the VM for both). T inside Docker could not be compared on the 1-CPU VM, and
stdin polling is off there by design; the relay chain is an emulation of the
docker hops, not the judge.

Stdin polling alone (same binary, `MVDB_SPIN_US=0` against the default, five
alternating query-phase runs through `relay 3`): 0.337 s against 0.317 s
median, single runs 0.325-0.360 s and 0.276-0.337 s. The request round trip
through the chain did not separate: p50 59-91 us without polling and 62-74 us
with it over six alternating runs, and two polling runs had a worse p99
(0.6-0.8 ms). On two hardware threads of one core the gain is therefore small
and not certain; with one CPU the reader does not poll at all.

Differential runs, all with zero mismatches: 150 seeds x 400 requests after
the literal-parser change; 400 seeds after the INSERT fast path (200 with the
fast path forced for every INSERT and two workers); on Linux 120 seeds with
chunked request delivery, then for the final binary 150 chunked seeds and 100
seeds with the fast path forced and a 50 us poll window. About 364,000
compared responses in total, 305,000 of them against binaries containing the
INSERT fast path.

Tried and not kept:

- Parsing INSERT rows while the request line is still arriving
  (branch `perf/stream-insert-experiment`): correct in the differential
  tests, but slower end to end (relay chain write 1.38-1.45 s against
  1.12-1.20 s; direct pipes also slower).
- Profile-guided build (`default.pgo` from the workload): no measurable
  change. Go 1.24 from the Ubuntu archive: about 5-8 % less parse time in
  microbenchmarks; not adopted, it would change the pinned toolchain.
- Lower GC target (GOGC 10 or 5) and an off-heap request buffer: no change in
  `memory.peak` beyond noise.
