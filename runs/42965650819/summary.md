| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 89f6d54 write split sync_calls=None sync=0.000 floor_samesize=0.142 -->
<!-- 89f6d54 query split unf=0.427 fil=0.494 floor_samesize=0.195 floor_ping=0.127 -->
| 89f6d54 | 3 | 0.369 | 5.6 | 12.6 | 0.920 | 5.6 | 22.0 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- 0c6d068 write split sync_calls=101 sync=0.037 floor_samesize=0.152 -->
<!-- 0c6d068 query split unf=0.319 fil=0.213 floor_samesize=0.174 floor_ping=0.130 -->
| 0c6d068 | 3 | 0.363 | 5.6 | 10.7 | 0.545 | 5.6 | 18.3 | 1.000/1.000 | 0/0 | 0.5603 |
<!-- b410e23 write split sync_calls=101 sync=0.031 floor_samesize=0.085 -->
<!-- b410e23 query split unf=0.167 fil=0.104 floor_samesize=0.152 floor_ping=0.116 -->
| b410e23 | 3 | 0.255 | 5.6 | 10.6 | 0.272 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.6359 |
