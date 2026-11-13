| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- b410e23 write split sync_calls=101 sync=0.031 floor_samesize=0.143 -->
<!-- b410e23 query split unf=0.251 fil=0.178 floor_samesize=0.277 floor_ping=0.199 -->
| b410e23 | 3 | 0.404 | 5.6 | 10.5 | 0.430 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- e3d67a8 write split sync_calls=101 sync=0.030 floor_samesize=0.125 -->
<!-- e3d67a8 query split unf=0.184 fil=0.150 floor_samesize=0.251 floor_ping=0.197 -->
| e3d67a8 | 3 | 0.249 | 5.6 | 10.1 | 0.337 | 5.6 | 14.9 | 1.000/1.000 | 0/0 | 0.5545 |
