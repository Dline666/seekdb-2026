| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- b410e23 write split sync_calls=101 sync=0.042 floor_samesize=0.103 -->
<!-- b410e23 query split unf=0.166 fil=0.107 floor_samesize=0.153 floor_ping=0.116 -->
| b410e23 | 3 | 0.284 | 5.6 | 10.5 | 0.274 | 5.6 | 18.4 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- b7ba357 write split sync_calls=101 sync=0.037 floor_samesize=0.088 -->
<!-- b7ba357 query split unf=0.162 fil=0.099 floor_samesize=0.138 floor_ping=0.114 -->
| b7ba357 | 3 | 0.194 | 5.6 | 10.4 | 0.261 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.5399 |
