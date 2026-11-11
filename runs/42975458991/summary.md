| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 89f6d54 write split sync_calls=None sync=0.000 floor_samesize=0.234 -->
<!-- 89f6d54 query split unf=0.667 fil=0.733 floor_samesize=0.384 floor_ping=0.257 -->
| 89f6d54 | 3 | 0.563 | 5.6 | 12.6 | 1.401 | 5.6 | 21.8 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- 0c6d068 write split sync_calls=101 sync=0.040 floor_samesize=0.244 -->
<!-- 0c6d068 query split unf=0.532 fil=0.277 floor_samesize=0.358 floor_ping=0.251 -->
| 0c6d068 | 3 | 0.534 | 5.6 | 10.7 | 0.811 | 5.6 | 18.2 | 1.000/1.000 | 0/0 | 0.5642 |
<!-- b410e23 write split sync_calls=101 sync=0.039 floor_samesize=0.157 -->
<!-- b410e23 query split unf=0.271 fil=0.188 floor_samesize=0.309 floor_ping=0.231 -->
| b410e23 | 3 | 0.402 | 5.6 | 10.5 | 0.458 | 5.6 | 18.3 | 1.000/1.000 | 0/0 | 0.6263 |
<!-- 3a450c0 write split sync_calls=101 sync=0.040 floor_samesize=0.143 -->
<!-- 3a450c0 query split unf=0.269 fil=0.187 floor_samesize=0.295 floor_ping=0.230 -->
| 3a450c0 | 3 | 0.359 | 5.6 | 10.5 | 0.456 | 5.6 | 18.2 | 1.000/1.000 | 0/0 | 0.6321 |
