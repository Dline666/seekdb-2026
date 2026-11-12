| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- f75bd58 write split sync_calls=101 sync=0.030 floor_samesize=0.123 -->
<!-- f75bd58 query split unf=0.251 fil=0.172 floor_samesize=0.248 floor_ping=0.195 -->
| f75bd58 | 3 | 0.251 | 5.6 | 10.0 | 0.421 | 5.6 | 10.9 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- bb4a134 write split sync_calls=101 sync=0.031 floor_samesize=0.123 -->
<!-- bb4a134 query split unf=0.227 fil=0.164 floor_samesize=0.250 floor_ping=0.195 -->
| bb4a134 | 3 | 0.254 | 5.6 | 9.9 | 0.393 | 5.6 | 10.6 | 1.000/1.000 | 0/0 | 0.5075 |
<!-- 07a7714 write split sync_calls=101 sync=0.028 floor_samesize=0.123 -->
<!-- 07a7714 query split unf=0.234 fil=0.166 floor_samesize=0.247 floor_ping=0.194 -->
| 07a7714 | 3 | 0.242 | 5.6 | 10.0 | 0.401 | 5.6 | 11.0 | 1.000/1.000 | 0/0 | 0.5066 |
<!-- e8eca93 write split sync_calls=101 sync=0.030 floor_samesize=0.122 -->
<!-- e8eca93 query split unf=0.229 fil=0.171 floor_samesize=0.248 floor_ping=0.196 -->
| e8eca93 | 3 | 0.240 | 5.6 | 10.2 | 0.402 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.5067 |
