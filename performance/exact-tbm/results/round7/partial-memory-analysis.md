# ROUND7 partial-safe-large memory and budget evidence

This document analyzes only the completed `partial-safe-large` comparison. It is not a report of the final partial-balanced stress results, and it does not incorporate the balanced-versus-safe comparison. No official score is estimated here.

## Sources and scope

- `/tmp/seekdb-round7/results/partial-safe-large-summary.json`
- `/tmp/seekdb-round7/results/partial-safe-large.jsonl`
- `/tmp/seekdb-round7/results/partial-safe-large-run.json`
- Fixture: completed ROUND6 log, `/tmp/seekdb-round6/heap-stress/db/data.log`.
- Baseline: `4a257014c9f43fd1dbb86b008d32cd93303b4d94` (typed heap).
- Baseline binary SHA-256: `b52c80e51e26b5edd4c30fe2d377115f34d4de02ca6dff1a38888e872b12e98b`.
- Partial-safe binary SHA-256: `30447f5b37ac18cd31d9776c923acbdb256170c0b8eec2a657df5be4bbbc1983`.

The completed comparison has five alternating rounds, 120 independently checked responses, six unique oracle SQL cases, and zero mismatches. All recorded OOM, OOM-kill, and group-kill counters are zero.

The fixture began with 550,000 vectors and ends with **549,997 live rows**, dimension 1024. Live raw vector bytes are **2,252,787,712**, exceeding the 2 GiB cgroup limit; the original pre-delete raw vector total was 2,252,800,000 bytes. All live categories in this final fixture are **c9**. Therefore the c9 filter covers the entire live table, not a 10% subset.

The comparison uses a 2 GiB cgroup (`2147483648` bytes), no swap, two CPUs, a 256 MiB index allowance, `GOMAXPROCS=2`, `GOGC=20`, and `GOMEMLIMIT=1288490188`. Queries retain exact semantics and the persistence setting is fdatasync. Each run has a private inode and verified cold cache before startup and before the cold query batch. Each cold/replay batch contains six queries. Replay uses the same process and remaining cache, without another eviction; it does not mean the entire dataset is resident.

## Five-round phase medians

T is elapsed seconds. B is measured cgroup read bytes: every write-byte measurement in this comparison is zero. M is the reported phase memory peak in bytes. Medians are calculated independently for each metric across five rounds.

| Phase | Base T (s) | Safe T (s) | Base B | Safe B | Base M | Safe M |
|---|---:|---:|---:|---:|---:|---:|
| startup | 2.845387171 | 2.834732516 | 4313972736 | 4313972736 | 2147483648 | 2147483648 |
| cold | 19.339156702 | 17.999848970 | 30917009408 | 25427484672 | 2147487744 | 2147504128 |
| replay | 13.746173079 | 4.904374222 | 22094688256 | 9949163520 | 2147487744 | 2147487744 |
| shutdown | 0.015064318 | 0.021603019 | 0 | 0 | 2145333248 | 2147442688 |
| service_total | 35.348392233 | 25.693296862 | 57264369664 | 39690620928 | 2147487744 | 2147639296 |

`service_total` adds startup, cold, replay, and shutdown T/B, and takes their maximum M. It excludes oracle generation, eviction, external validation, and preparation. These phase medians need not sum to the median service total.

Changes calculated as ratios of the above medians:

| Phase | T change | B change | M change |
|---|---:|---:|---:|
| startup | -0.374454% | 0% | 0% |
| cold | -6.925368% | -17.755678% | +0.000763% |
| replay | -64.321894% | -54.970338% | 0% |
| shutdown | +43.405% (+0.006538701 s) | both zero | +0.098326% |
| service_total | -27.314100% | -30.688801% | +0.007057% |

The median of paired per-round ratios is a different statistic: cold T/B changes are -5.445541%/-18.253709%, replay -64.295153%/-57.051810%, and service_total -27.829066%/-31.848304%. Every round improves cold, replay, and service_total T and B. Paired service_total improvements range from 26.4508% to 35.5885% for T and 30.0356% to 35.4976% for B. The shutdown difference is only about 6.54 ms at the median and should not be overstated.

## All reported phase peaks and overshoots

The configured limit is exactly 2147483648 bytes. Each cell below is `reported M / max(M - limit, 0)` in bytes. Rounds are numbered 0 through 4. Values are retained as reported, including small overshoots; none is clamped to the configured limit.

| Version | Phase | Round 0 | Round 1 | Round 2 | Round 3 | Round 4 |
|---|---|---:|---:|---:|---:|---:|
| base | startup | 2147483648 / 0 | 2147483648 / 0 | 2147483648 / 0 | 2147483648 / 0 | 2147483648 / 0 |
| base | cold | 2147487744 / 4096 | 2147487744 / 4096 | 2147487744 / 4096 | 2147495936 / 12288 | 2147487744 / 4096 |
| base | replay | 2147487744 / 4096 | 2147487744 / 4096 | 2147487744 / 4096 | 2147487744 / 4096 | 2147487744 / 4096 |
| base | shutdown | 2144079872 / 0 | 2144821248 / 0 | 2145333248 / 0 | 2147467264 / 0 | 2145476608 / 0 |
| base | service_total | 2147487744 / 4096 | 2147487744 / 4096 | 2147487744 / 4096 | 2147495936 / 12288 | 2147487744 / 4096 |
| partial-safe | startup | 2147483648 / 0 | 2147483648 / 0 | 2147483648 / 0 | 2147483648 / 0 | 2147483648 / 0 |
| partial-safe | cold | 2147487744 / 4096 | 2147487744 / 4096 | 2147741696 / 258048 | 2147504128 / 20480 | 2147688448 / 204800 |
| partial-safe | replay | 2147581952 / 98304 | 2147487744 / 4096 | 2147487744 / 4096 | 2147639296 / 155648 | 2147487744 / 4096 |
| partial-safe | shutdown | 2147274752 / 0 | 2147471360 / 0 | 2147356672 / 0 | 2147442688 / 0 | 2147450880 / 0 |
| partial-safe | service_total | 2147581952 / 98304 | 2147487744 / 4096 | 2147741696 / 258048 | 2147639296 / 155648 | 2147688448 / 204800 |

The largest reported kernel phase M is **2147495936** for base (cold, round 3), an overshoot of **12288** bytes; for partial-safe it is **2147741696** (cold, round 2), an overshoot of **258048** bytes, or 2048.24609375 MiB total. These are observational results, not a claim that every measured current value was below the configured limit.

## Independent component maxima

The following anon and file values are independently maximized across all five rounds in each phase. They are **not additive** and need not occur at the same time. `service_total` combines different phases: its maximum file value originates in startup, while its maximum anon value originates in later phases.

| Phase | Base max anon | Base max file | Safe max anon | Safe max file |
|---|---:|---:|---:|---:|
| startup | 47788032 | 2113929216 | 48115712 | 2113929216 |
| cold | 58273792 | 2082127872 | 275107840 | 1870663680 |
| replay | 58368000 | 2075185152 | 275218432 | 1855160320 |
| shutdown | 58368000 | 2072977408 | 275218432 | 1854566400 |
| service_total | 58368000 | 2113929216 | 275218432 | 2113929216 |

## Same-record samples and measurement limits

The sampler runs every 50 ms. A tuple in this section comes from one sample record, but the underlying cgroup/process files are read sequentially, **not atomically**. These records are not decompositions of the exact `memory.peak` instant. Differences between sampled current and reported phase M must be retained rather than silently reconciled. In particular, some sampled current values exceed the reported phase M. `file_mapped` is a subset of file; process RSS overlaps mapped file cache. Neither should be added to file/current as an independent component.

Representative base records, selected by highest sampled current within each phase:

| Phase / round | Sampled current | Current above limit | anon | file | kernel | file_mapped | RSS | Reported phase M |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| startup / 1 | 2147483648 | 0 | 42455040 | 2103443456 | 401408 | 0 | 44703744 | 2147483648 |
| cold / 1 | 2147737600 | 253952 | 57991168 | 2074824704 | 14127104 | 2072924160 | 2133229568 | 2147487744 |
| replay / 3 | 2147667968 | 184320 | 58286080 | 2073759744 | 15368192 | 2058702848 | 2119626752 | 2147487744 |
| shutdown / 3 | 2147467264 | 0 | 58322944 | 2072977408 | 15396864 | 2031296512 | 2091933696 | 2147467264 |

Partial-safe selected records for every round and phase:

| Phase | Round | Sampled current | Current above limit | anon | file | kernel |
|---|---:|---:|---:|---:|---:|---:|
| startup | 0 | 2147307520 | 0 | 42700800 | 2103443456 | 401408 |
| startup | 1 | 2147225600 | 0 | 35807232 | 2109734912 | 450560 |
| startup | 2 | 2147225600 | 0 | 35733504 | 2109734912 | 450560 |
| startup | 3 | 2147356672 | 0 | 46489600 | 2099380224 | 507904 |
| startup | 4 | 2147287040 | 0 | 42639360 | 2103443456 | 397312 |
| cold | 0 | 2147483648 | 0 | 267583488 | 1870360576 | 9154560 |
| cold | 1 | 2147618816 | 135168 | 275062784 | 1855094784 | 17190912 |
| cold | 2 | 2147622912 | 139264 | 274649088 | 1857593344 | 14905344 |
| cold | 3 | 2147733504 | 249856 | 274636800 | 1858736128 | 13910016 |
| cold | 4 | 2147508224 | 24576 | 274853888 | 1855479808 | 16957440 |
| replay | 0 | 2147667968 | 184320 | 275144704 | 1853714432 | 18419712 |
| replay | 1 | 2147483648 | 0 | 275075072 | 1854558208 | 17715200 |
| replay | 2 | 2147696640 | 212992 | 274960384 | 1854504960 | 17891328 |
| replay | 3 | 2147483648 | 0 | 274882560 | 1855086592 | 17412096 |
| replay | 4 | 2147483648 | 0 | 275013632 | 1854619648 | 17784832 |
| shutdown | 0 | 2147274752 | 0 | 275169280 | 1853513728 | 18583552 |
| shutdown | 1 | 2147471360 | 0 | 275218432 | 1853890560 | 18313216 |
| shutdown | 2 | 2147356672 | 0 | 274993152 | 1853718528 | 18382848 |
| shutdown | 3 | 2147442688 | 0 | 275050496 | 1854566400 | 17817600 |
| shutdown | 4 | 2147450880 | 0 | 275091456 | 1854160896 | 18190336 |

Additional fields for partial-safe records with the highest sampled current within each phase:

| Phase / round | file_mapped | RSS | Reported phase M |
|---|---:|---:|---:|
| startup / 3 | 0 | 48807936 | 2147483648 |
| cold / 3 | 1032024064 | 1308979200 | 2147504128 |
| replay / 2 | 1323597824 | 1600745472 | 2147487744 |
| shutdown / 1 | 1073786880 | 1351323648 | 2147471360 |

## Exact live-row budget calculation

This calculation uses **549997**, not the original 550000 rows. Array estimates are based on retained capacity; the slot map is an estimate tracked using its allocation hint / entry high-water mark. Deletion does not make the historical map capacity charge disappear.

```text
N = 549997
D = 1024
index budget B = 268435456
headroom = B / 16 = 16777216
slot metadata estimate = 64 * N = 35199808
scratch = 4 * D = 4096
available for quantization blocks
  = B - headroom - 64*N - 4*D
  = 216454336
bytes per 128-row block
  = 128 * (D + 32) + 16
  = 135184
complete blocks = floor(216454336 / 135184) = 1601
Q = 1601 * 128 = 204928
coverage = Q / N = 37.25983959912509%

codes = Q * D = 209846272
qf = 32 * Q = 6557696
qbounds = 16 * 1601 = 25616
quantization plus scratch = 216433680
total derived estimate before equality cache
  = 35199808 + 216433680
  = 251633488
unallocated index budget = 268435456 - 251633488 = 16801968

full quantization estimate
  = N * (64 + D + 32) + 4*D + 16*ceil(N/128)
  = 616069488
live raw vector bytes = N * D * 4 = 2252787712
```

The B/16 headroom is unallocated budget, not a protected memory reservation or a hard process/cgroup RSS guarantee. The slot metadata calculation remains an estimate, not an allocator-level proof of total resident memory.

## Interpretation and boundaries

Partial codes eliminate some exact vector reads and markedly reduce replay I/O while retaining the derived state. Startup alone reads 4,313,972,736 bytes and already fills the cgroup close to 2 GiB. During queries, the independent replay anon maximum rises by 216,850,432 bytes, from 58,368,000 to 275,218,432. That increase is close in magnitude to the planned 216,433,680 bytes of quantization-plus-scratch allocation, but it is not an exact object attribution: anon also includes runtime allocations, garbage awaiting reclamation, stacks, and other anonymous memory. No Go heap profile was taken for this analysis.

The extra anonymous state displaces file cache, so substantial reductions in read bytes coexist with M remaining near the cgroup limit. Independent maxima and sequential same-record samples must not be summed to manufacture a precise memory decomposition.

This completed comparison measures startup, query, replay, and shutdown behavior on the final all-c9 fixture. It does not supply write-phase performance, a formal request deadline, or evidence of a completely resident replay dataset. The correctness checks and zero OOM counters are limited to these measured runs. No official submission was made and no official performance score is inferred. Balanced stress or update/recovery conclusions require their own completed result artifacts.
