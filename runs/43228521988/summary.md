| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- f75bd58 write split sync_calls=101 sync=0.047 floor_samesize=0.106 -->
<!-- f75bd58 query split unf=0.250 fil=0.171 floor_samesize=0.192 floor_ping=0.159 -->
| f75bd58 | 3 | 0.232 | 5.6 | 10.1 | 0.422 | 5.6 | 10.9 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- bb4a134 write split sync_calls=101 sync=0.047 floor_samesize=0.105 -->
<!-- bb4a134 query split unf=0.241 fil=0.167 floor_samesize=0.201 floor_ping=0.156 -->
| bb4a134 | 3 | 0.245 | 5.6 | 10.1 | 0.413 | 5.6 | 10.9 | 1.000/1.000 | 0/0 | 0.4995 |
