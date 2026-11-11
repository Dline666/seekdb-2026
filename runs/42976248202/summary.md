| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 89f6d54 write split sync_calls=None sync=0.000 floor_samesize=0.197 -->
<!-- 89f6d54 query split unf=0.535 fil=0.612 floor_samesize=0.279 floor_ping=0.177 -->
| 89f6d54 | 3 | 0.582 | 5.6 | 12.6 | 1.148 | 5.6 | 21.9 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- b410e23 write split sync_calls=101 sync=0.033 floor_samesize=0.112 -->
<!-- b410e23 query split unf=0.195 fil=0.139 floor_samesize=0.216 floor_ping=0.153 -->
| b410e23 | 3 | 0.341 | 5.6 | 10.6 | 0.335 | 5.6 | 18.0 | 1.000/1.000 | 0/0 | 0.6444 |
<!-- 3a450c0 write split sync_calls=101 sync=0.041 floor_samesize=0.112 -->
<!-- 3a450c0 query split unf=0.194 fil=0.137 floor_samesize=0.213 floor_ping=0.154 -->
| 3a450c0 | 3 | 0.309 | 5.6 | 10.6 | 0.333 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.6491 |
<!-- a1cedd3 write split sync_calls=101 sync=0.040 floor_samesize=0.100 -->
<!-- a1cedd3 query split unf=0.197 fil=0.138 floor_samesize=0.211 floor_ping=0.154 -->
| a1cedd3 | 3 | 0.281 | 5.6 | 10.4 | 0.334 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.6534 |
