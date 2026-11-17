# 2026-11-17 performance experiment handoff

The user requests extreme optimization of Dline666/seekdb-2026's MiniVectorDB,
plus a 2026 Go lint/format tool. They then explicitly requested moving the entire
experiment to the SSH server and starting Codex with YOLO, gpt-6-astra and max
reasoning. After successful migration and confirmed remote startup, remove local
build artifacts and both task WSL distributions; the Windows coordinating agent
owns that cleanup. No official competition submission or GitHub push is requested.

## Repository and rules

Local source was C:/Users/27372/seekdb-perf-20261117, branch
perf/extreme-20261117, based on the public round7 branch at
3192a2add06d8d8036884c1ed541e7ddca5df9fb. This is the actual standalone Go
competition implementation, not the upstream C++ master branch.

Official rules:
https://oceanbase.yuque.com/org-wiki-obtech-vh7w9r/hc3pio/lg8enxlmwg69unt6
Page metadata checked on 2026-11-17, published content date 2026-11-11.
The current JSONL protocol is STDIO_PROTOCOL.md and protocol_check/config.json.
Default benchmark: 10,000 rows, 128 dimensions, 1,000 TopK(10) queries, half
filtered. Correct SQL, exact search, atomic transactions and durable acknowledged
commits are mandatory. E combines time/I/O/memory at .6/.2/.2, write/query 1:2;
use five alternating A/B rounds locally. memory.peak includes page cache.
Local E is a relative diagnostic, never a prediction of official raw scores.
Do not fabricate learning evidence or modify old reports to imply new results.

## Changes already in the working tree

1. New four-row AVX-VNNI kernels, sequential and gathered candidates, including
   scalar tails, unchanged float64 association, exact int32 dots and unordered
   comparison behavior. CPU-gated. MVDB_NO_QUAD_VNNI=1 disables these kernels.
   Independent scalar survivor tests cover partial groups, nonzero offsets,
   repeated/reversed candidates, ties, thresholds at adjacent float64 values,
   exceptional folded constants and survivor-buffer sentinels.
2. Reused changes from existing local perf/exact-round2-v3, not newly invented:
   fa07e43 (decimal/vector parser), a39fc2c (fast INSERT), 5a854ae and cc09d6c
   (JSON request decoder), 7d65ed1 (release retained INSERT source strings).
   Kept the Round7 typed heap and partial budgeted index. Did not enable stdin
   busy polling. Original fdatasync and precise numeric semantics remain.
3. EXPERIMENTAL residual norm bounds: qparam.errScale and queryResidualScale,
   using the old 4-byte padding in quantBlockBound (still 16 bytes). Full bound
   derivation and independent rational oracle coverage remain to be completed.
   MVDB_NO_RESIDUAL_BOUND=1 restores original bounds. Consider removing this
   change if measured benefits or proof do not justify it. It has passed the
   existing full test suite and paired benchmark truths, but that is not enough
   to claim all extreme-number/resource cases are covered.
4. golangci-lint 2.15.0 (released 2026-10-16), gofumpt + goimports, govet,
   ineffassign, staticcheck, unused. .golangci.yml and Makefile in minivectordb.
   Formatting applied, nine reported issues fixed, final run: 0 issues.
   Go remains 1.22.2 for binary comparisons. The linter binary was verified
   against the published SHA256. Read README for the exact pinned checksum.

## Snapshots and measurements

`experiment-runtime/` contains distinct minivectordb source snapshots and `bin/`:
base, quad, combined, quadboth, residual, handoff. All original build/source hash
records and raw measurements are in this directory's results/. Original scripts
used /tmp/seekdb-extreme-20261117; these strings in old reports are historical.
handoff matches current Go code after formatting/lint fixes. It has full Go test
PASS; formatting-only changes occurred after residual's measured binary.

Local hardware: Intel Core Ultra 5 125H, WSL2, 2 CPU / 2 GiB / no swap cgroups,
Go 1.22.2, GOGC=20, exact, fdatasync. Serial experiment runs. Native pipes; no
new Docker comparison yet. Write wall time was noisy due to local filesystem
latency, so do not overinterpret write results.

| Five-round comparison | Query time medians | Scope |
|---|---|---|
| base -> quad | .308661 -> .330675 s | 10k x128 /3000, regression retained |
| quad disabled -> enabled, same binary | 1.027351 -> 1.005671 s | /10000 |
| base -> combined | .536462 -> .514465 s | /5000 |
| base -> quadboth | .515094 -> .499048 s | /5000 |
| quadboth -> residual | .454279 -> .456916 s | /5000, neutral/slower |
| quadboth -> residual | .420623 -> .399299 s | 100k x256 /500 |

Microbenchmark four-row sequential 128D: ~285 ->175 ns/128 rows, not end-to-end.
All paired query oracles passed; raw JSONL records include per-stage T/B/M and
hashes. Review the summaries rather than reporting only favorable measurements.

## Remote continuation

Remote root: /home/platemo/seekdb-experiments/20261117
Repository: repository/
Go toolchain: toolchains/go/bin/go
Pinned linter: experiment-runtime/lint/golangci-lint-2.15.0-linux-amd64/golangci-lint

Set MVDB_LAB_ROOT and MVDB_LAB_GO to the above absolute paths before importing
lab.py. `lab.py build NAME` snapshots/builds; `lab.py bench LABEL --bins A,B`
runs the paired cgroup driver. It requires root for cgroup setup: sudo -n works
for this account. Invoke only benchmark/test helpers under sudo, keep Codex and
repository editing as platemo. The remote host has Docker and GNU screen.
Use new output labels, never overwrite old experiments. Existing snapshot binaries
are portable GOAMD64=v1 with runtime CPU gates; regenerate final binaries locally
on this server using the pinned compiler. Driver source and analyzer are bundled.

Remote CPU is AMD Threadripper PRO 5975WX, 32 cores/64 threads, 256 GiB RAM;
it has AVX2/FMA but NO AVX-VNNI/AVX512. Thus new VNNI kernels should stay gated
off here. Prioritize portable AVX2 and algorithmic/parse/I/O improvements; do not
pretend to have revalidated the Intel-specific instruction paths on AMD.

Remaining work: run fresh same-machine baseline vs candidates, finish rational
proof/oracle for residual bounds or reject them, seek further measured gains,
test fallback paths/race/crash/public Docker protocol, and repeat a >2 GiB data
workflow with 2 GiB limits. Historical capacity scripts and full prior reports
are already under performance/exact-tbm. The archived last complete large fixture
may also be supplied in migration archives; its data must not become production
answers. Count CPU and I/O consistently, avoid concurrent benchmark interference,
preserve exactness and durability. Run make fmt and make lint, plus final tests
against the final source, and create a concise Chinese report of retained/rejected
experiments, T/B/M, risks, reproducible commands and pending formal evaluation.

Do not publish, push or formally submit without later user authorization. The
requested work is active remote experimentation, not simply writing a plan.
