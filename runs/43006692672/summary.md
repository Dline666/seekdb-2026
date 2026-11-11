| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 89f6d54 write split sync_calls=None sync=0.000 floor_samesize=0.192 -->
<!-- 89f6d54 query split unf=0.532 fil=0.603 floor_samesize=0.275 floor_ping=0.173 -->
| 89f6d54 | 3 | 0.509 | 5.6 | 12.5 | 1.136 | 5.6 | 22.0 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- b410e23 write split sync_calls=101 sync=0.032 floor_samesize=0.111 -->
<!-- b410e23 query split unf=0.193 fil=0.136 floor_samesize=0.212 floor_ping=0.151 -->
| b410e23 | 3 | 0.316 | 5.6 | 10.5 | 0.331 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.6418 |
<!-- 663ebf4 write split sync_calls=101 sync=0.092 floor_samesize=0.114 -->
<!-- 663ebf4 query split unf=0.197 fil=0.135 floor_samesize=0.203 floor_ping=0.148 -->
| 663ebf4 | 3 | 0.315 | 5.6 | 10.2 | 0.333 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.6571 |
<!-- efdb88e write split sync_calls=101 sync=0.047 floor_samesize=0.100 -->
<!-- efdb88e query split unf=0.196 fil=0.133 floor_samesize=0.185 floor_ping=0.149 -->
| efdb88e | 3 | 0.229 | 5.6 | 10.3 | 0.329 | 5.6 | 10.6 | 1.000/1.000 | 0/0 | 0.6727 |
