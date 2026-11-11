| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- b410e23 write split sync_calls=101 sync=0.053 floor_samesize=0.153 -->
<!-- b410e23 query split unf=0.273 fil=0.190 floor_samesize=0.284 floor_ping=0.205 -->
| b410e23 | 3 | 0.436 | 5.6 | 10.5 | 0.463 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- 8013d47 write split sync_calls=101 sync=0.053 floor_samesize=0.154 -->
<!-- 8013d47 query split unf=0.265 fil=0.191 floor_samesize=0.285 floor_ping=0.207 -->
| 8013d47 | 3 | 0.435 | 5.6 | 10.5 | 0.457 | 5.6 | 18.2 | 1.000/1.000 | 0/0 | 0.5014 |
