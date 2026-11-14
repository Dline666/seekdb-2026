This Linux-only driver compares generic SQL without importing database code or
using a baseline response as truth. It is a local T/B/M comparison, not an
official evaluator or a predicted platform score.

The existing large fixture must be the **completed ROUND3 final** database:
550,000 original rows, dimension 1024, replacements of IDs 1–16 and 18, committed
deletes of IDs `rows/3`, `rows/2`, and `rows-1`, and a final bulk UPDATE setting all
live categories to `c9`. IDs 17 and 19 retain their original vectors; the killed
insert at `rows+1` is absent. Expected coordinates come from the documented
row-local generator, and norms use an independent scalar float64 sum. A point
query compares against its expected full vector and requires distance exactly
zero. Ordered pages and census check every returned primary key, category, and
norm. The census does **not** compare every vector coordinate.

An existing fixture is copied to a fresh private work directory with a distinct
inode. The driver never starts a database on the original path. It records a
SHA256 of the private copy before/after the run and checks size/mtime after each
trial; any unexpected rewrite or truncation fails the experiment. The source
size/mtime is checked at the end. With no `-fixture`, preparation generates a
small database containing the same final logical model directly. That preparation
does not reproduce the large fixture's history or measure write performance.

Each trial starts a new process within the same cgroup: 2 CPUs, 2GiB, no swap,
128 PIDs. Sync is explicitly fdatasync; the index budget is 256MiB,
GOMAXPROCS=2, GOMEMLIMIT=1288490188, GOGC=20. Processes and page-cache charges
remain in that cgroup across restarts. Only the private fixture's cache is
evicted via Sync/FADV_DONTNEED, with mincore requiring at most one resident
partial page. This occurs **before startup and again after ready**, so the measured
SQL group starts with cold row pages even though recovery has just read the log.
If a binary keeps pages mapped in a way that prevents eviction, the trial fails
rather than silently recording a warm run. No system-wide drop_caches is used.

Each version runs the same fixed SQL group for three rounds, with version order
reversed on odd rounds. The default existing-fixture profile executes one point
query (`id=18`) and one ordered page (`ORDER BY id LIMIT 10 OFFSET live_rows/2`)
per respective group. Small generated fixtures use extended cases: committed,
rolled-back, deleted, and absent point rows, true/false residual predicates,
ascending/descending order, implicit primary-key order, ordinal order, filtering,
offsets beyond the end, and LIMIT 0. `-profile minimal` or `extended` overrides
this choice. Query text and its hash are recorded so runs can be compared by
identical workload.

The census is one complete `ORDER BY id` result by default, **one diagnostic round
per version**, separate from the repeated point/order comparisons. The old large
census took roughly ten minutes; the point and limited-order baseline also read
the full table, so their runtime must not be assumed cheap. There is no request
timeout and incomplete runs must not be scored as completed work. `-suites` and
`-census-rounds 0` allow scheduling smaller experiments first.

Per-trial records are `<suite>_startup`, `<suite>_query`, and `<suite>_shutdown`.
Startup T starts before exec. Query T includes JSONL requests, protocol round
trips, and response output; SQL/vector construction, oracle generation, and
response decoding/verification are outside T. The driver snapshots B/M before
verification and emits the query result only after it passes. B is the data
device's cgroup physical read/write delta. M is cgroup memory.peak, reset/read
using the same open descriptor; anon/file/RSS are separately sampled every
50ms. Samples may miss instantaneous maxima. CPU and memory.events fields are
cumulative cgroup counters. Successful runs require no OOM event.

Report query-only T/B/M and startup separately. The `measured_stage_total` field
is the **startup + query + shutdown service-stage sum**（服务阶段和）. It includes
the artificial forced-cold SQL after startup and excludes fixture preparation,
cold-cache eviction, and external truth generation/validation. It is **not the
complete driver's wall clock**. A stage E may use weights 0.6/0.2/0.2 and
1ms/1MiB smoothing, with the first three reference rounds' medians. Do not infer
official write/query weights for these generic SQL workloads or extrapolate a
platform score. The one-round census is diagnostic and has no stable median.

Build and run the small comparison first, after coordinating CPU use:

```sh
go build -o /tmp/sql-compare performance/exact-tbm/sqlcompare/main.go
/tmp/sql-compare -bins /tmp/bin/baseline,/tmp/bin/candidate \
  -work-dir /tmp/sql-small-private -rows 10000 -dim 128 \
  -rounds 3 -census-rounds 3 > /tmp/sql-small.jsonl
```

The private directory must not already exist. A functional smoke test can use
`-rows 100 -dim 16 -rounds 1 -census-rounds 1`. For the existing large fixture:

```sh
/tmp/sql-compare -bins /tmp/bin/baseline,/tmp/bin/candidate \
  -fixture /tmp/seekdb-round3-stress-final/db/data.log \
  -work-dir /tmp/sql-large-private -rows 550000 -dim 1024 \
  -rounds 3 -census-rounds 1 > /tmp/sql-large.jsonl
```

Run individual suites separately when coordinating long baselines:

```sh
/tmp/sql-compare -bins /tmp/bin/baseline,/tmp/bin/candidate \
  -fixture /tmp/seekdb-round3-stress-final/db/data.log \
  -work-dir /tmp/sql-large-census-private -rows 550000 -dim 1024 \
  -suites census -census-rounds 1 > /tmp/sql-large-census.jsonl
python3 performance/exact-tbm/sqlcompare/analyze.py --baseline baseline \
  /tmp/sql-small.jsonl > /tmp/sql-small-summary.json
```

The analyzer requires a final `success` marker, a separate `query_validation`
record for each query phase, matching SQL hashes across all versions/rounds,
matching validation counts, phase peaks reset through one FD, cold row pages,
and no OOM event. It reports query-only and measured-stage-total medians and
stage E against the first three baseline rounds' fixed medians. A single-round
census is labeled `single_diagnostic`, with no claim of a stable speedup.
Retain complete JSONL and stderr files, including failures, with the experiment.
