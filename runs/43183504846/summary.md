| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- ba5b0fe write split sync_calls=101 sync=0.031 floor_samesize=0.078 -->
<!-- ba5b0fe query split unf=0.169 fil=0.102 floor_samesize=0.140 floor_ping=0.113 -->
| ba5b0fe | 3 | 0.189 | 5.6 | 10.4 | 0.271 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- 3e4fa54 write split sync_calls=101 sync=0.030 floor_samesize=0.076 -->
<!-- 3e4fa54 query split unf=0.187 fil=0.115 floor_samesize=0.142 floor_ping=0.117 -->
| 3e4fa54 | 3 | 0.180 | 5.6 | 10.1 | 0.303 | 5.6 | 10.9 | 1.000/1.000 | 0/0 | 0.4915 |
