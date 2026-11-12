| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 3e4fa54 write split sync_calls=101 sync=0.049 floor_samesize=0.096 -->
<!-- 3e4fa54 query split unf=0.197 fil=0.136 floor_samesize=0.187 floor_ping=0.150 -->
| 3e4fa54 | 3 | 0.230 | 5.6 | 9.8 | 0.334 | 5.6 | 10.9 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- ba5b0fe write split sync_calls=101 sync=0.085 floor_samesize=0.112 -->
<!-- ba5b0fe query split unf=0.200 fil=0.137 floor_samesize=0.191 floor_ping=0.151 -->
| ba5b0fe | 3 | 0.291 | 5.6 | 10.2 | 0.340 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.4862 |
