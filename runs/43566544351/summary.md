| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- b44b5b4 write split sync_calls=101 sync=0.036 floor_samesize=0.125 -->
<!-- b44b5b4 query split unf=0.245 fil=0.176 floor_samesize=0.266 floor_ping=0.226 -->
| b44b5b4 | 3 | 0.259 | 5.6 | 10.1 | 0.422 | 5.6 | 10.8 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- e3d67a8 write split sync_calls=101 sync=0.039 floor_samesize=0.120 -->
<!-- e3d67a8 query split unf=1.050 fil=0.332 floor_samesize=0.266 floor_ping=0.225 -->
| e3d67a8 | 3 | 0.254 | 5.6 | 10.1 | 1.382 | 5.6 | 15.0 | 1.000/1.000 | 0/0 | 0.3846 |
<!-- 33485b5 write split sync_calls=101 sync=0.039 floor_samesize=0.100 -->
<!-- 33485b5 query split unf=0.186 fil=0.163 floor_samesize=0.262 floor_ping=0.226 -->
| 33485b5 | 3 | 0.235 | 5.6 | 9.7 | 0.350 | 5.6 | 12.1 | 1.000/1.000 | 0/0 | 0.5206 |
