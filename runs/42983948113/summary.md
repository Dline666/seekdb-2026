| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 89f6d54 write split sync_calls=None sync=0.000 floor_samesize=0.143 -->
<!-- 89f6d54 query split unf=0.424 fil=0.497 floor_samesize=0.197 floor_ping=0.129 -->
| 89f6d54 | 3 | 0.360 | 5.6 | 12.7 | 0.921 | 5.6 | 22.0 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- b410e23 write split sync_calls=101 sync=0.031 floor_samesize=0.100 -->
<!-- b410e23 query split unf=0.166 fil=0.104 floor_samesize=0.153 floor_ping=0.114 -->
| b410e23 | 3 | 0.258 | 5.6 | 10.5 | 0.271 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.6344 |
<!-- a1cedd3 write split sync_calls=101 sync=0.031 floor_samesize=0.093 -->
<!-- a1cedd3 query split unf=0.169 fil=0.103 floor_samesize=0.151 floor_ping=0.113 -->
| a1cedd3 | 3 | 0.232 | 5.6 | 10.3 | 0.270 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.6399 |
<!-- a871971 write split sync_calls=101 sync=0.031 floor_samesize=0.077 -->
<!-- a871971 query split unf=0.165 fil=0.100 floor_samesize=0.143 floor_ping=0.112 -->
| a871971 | 3 | 0.229 | 5.6 | 10.3 | 0.268 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.6565 |
