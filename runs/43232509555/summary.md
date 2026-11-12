| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- f75bd58 write split sync_calls=101 sync=0.038 floor_samesize=0.121 -->
<!-- f75bd58 query split unf=0.260 fil=0.179 floor_samesize=0.264 floor_ping=0.225 -->
| f75bd58 | 5 | 0.255 | 5.6 | 10.1 | 0.441 | 5.6 | 10.7 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- bb4a134 write split sync_calls=101 sync=0.037 floor_samesize=0.126 -->
<!-- bb4a134 query split unf=0.245 fil=0.173 floor_samesize=0.263 floor_ping=0.227 -->
| bb4a134 | 5 | 0.256 | 5.6 | 10.0 | 0.420 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.5045 |
<!-- e47dd55 write split sync_calls=101 sync=0.036 floor_samesize=0.120 -->
<!-- e47dd55 query split unf=0.246 fil=0.176 floor_samesize=0.263 floor_ping=0.225 -->
| e47dd55 | 5 | 0.250 | 5.6 | 10.1 | 0.422 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.5049 |
