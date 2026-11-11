| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 89f6d54 write split sync_calls=None sync=0.000 floor_samesize=0.261 -->
<!-- 89f6d54 query split unf=0.685 fil=0.769 floor_samesize=0.346 floor_ping=0.228 -->
| 89f6d54 | 3 | 0.602 | 5.6 | 12.5 | 1.455 | 5.6 | 21.9 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- 0c6d068 write split sync_calls=101 sync=0.028 floor_samesize=0.264 -->
<!-- 0c6d068 query split unf=0.519 fil=0.281 floor_samesize=0.331 floor_ping=0.222 -->
| 0c6d068 | 3 | 0.527 | 5.6 | 10.5 | 0.801 | 5.6 | 18.5 | 1.000/1.000 | 0/0 | 0.5727 |
<!-- b410e23 write split sync_calls=101 sync=0.027 floor_samesize=0.151 -->
<!-- b410e23 query split unf=0.249 fil=0.172 floor_samesize=0.271 floor_ping=0.192 -->
| b410e23 | 3 | 0.395 | 5.6 | 10.7 | 0.422 | 5.6 | 18.2 | 1.000/1.000 | 0/0 | 0.6389 |
<!-- 663ebf4 write split sync_calls=101 sync=0.027 floor_samesize=0.127 -->
<!-- 663ebf4 query split unf=0.254 fil=0.173 floor_samesize=0.261 floor_ping=0.190 -->
| 663ebf4 | 3 | 0.323 | 5.6 | 10.3 | 0.428 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.6632 |
