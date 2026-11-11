| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 89f6d54 write split sync_calls=None sync=0.000 floor_samesize=0.235 -->
<!-- 89f6d54 query split unf=0.668 fil=0.749 floor_samesize=0.376 floor_ping=0.252 -->
| 89f6d54 | 3 | 0.587 | 5.6 | 12.6 | 1.418 | 5.6 | 22.4 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- b410e23 write split sync_calls=101 sync=0.038 floor_samesize=0.148 -->
<!-- b410e23 query split unf=0.279 fil=0.186 floor_samesize=0.304 floor_ping=0.226 -->
| b410e23 | 3 | 0.415 | 5.6 | 10.4 | 0.465 | 5.6 | 18.3 | 1.000/1.000 | 0/0 | 0.6276 |
<!-- a871971 write split sync_calls=101 sync=0.034 floor_samesize=0.134 -->
<!-- a871971 query split unf=0.289 fil=0.186 floor_samesize=0.289 floor_ping=0.224 -->
| a871971 | 3 | 0.356 | 5.6 | 10.1 | 0.477 | 5.6 | 11.1 | 1.000/1.000 | 0/0 | 0.6484 |
<!-- 663ebf4 write split sync_calls=101 sync=0.038 floor_samesize=0.126 -->
<!-- 663ebf4 query split unf=0.266 fil=0.181 floor_samesize=0.285 floor_ping=0.224 -->
| 663ebf4 | 3 | 0.323 | 5.6 | 10.5 | 0.448 | 5.6 | 10.9 | 1.000/1.000 | 0/0 | 0.6573 |
