| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- f75bd58 write split sync_calls=101 sync=0.030 floor_samesize=0.094 -->
<!-- f75bd58 query split unf=0.194 fil=0.131 floor_samesize=0.186 floor_ping=0.149 -->
| f75bd58 | 3 | 0.210 | 5.6 | 10.3 | 0.327 | 5.6 | 10.6 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- ba5b0fe write split sync_calls=101 sync=0.032 floor_samesize=0.099 -->
<!-- ba5b0fe query split unf=0.195 fil=0.131 floor_samesize=0.188 floor_ping=0.150 -->
| ba5b0fe | 3 | 0.236 | 5.6 | 10.2 | 0.327 | 5.6 | 10.9 | 1.000/1.000 | 0/0 | 0.4937 |
