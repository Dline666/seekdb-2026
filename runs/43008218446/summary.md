| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |
|---|---|---|---|---|---|---|---|---|---|---|
<!-- 89f6d54 write split sync_calls=None sync=0.000 floor_samesize=0.235 -->
<!-- 89f6d54 query split unf=0.770 fil=0.866 floor_samesize=0.343 floor_ping=0.226 -->
| 89f6d54 | 3 | 0.582 | 5.6 | 12.7 | 1.638 | 5.6 | 22.0 | 1.000/1.000 | 0/0 | 0.5000 |
<!-- b410e23 write split sync_calls=101 sync=0.054 floor_samesize=0.153 -->
<!-- b410e23 query split unf=0.279 fil=0.192 floor_samesize=0.270 floor_ping=0.190 -->
| b410e23 | 3 | 0.432 | 5.6 | 10.5 | 0.471 | 5.6 | 18.1 | 1.000/1.000 | 0/0 | 0.6344 |
<!-- 663ebf4 write split sync_calls=101 sync=0.053 floor_samesize=0.138 -->
<!-- 663ebf4 query split unf=0.280 fil=0.191 floor_samesize=0.254 floor_ping=0.194 -->
| 663ebf4 | 3 | 0.353 | 5.6 | 10.3 | 0.472 | 5.6 | 10.7 | 1.000/1.000 | 0/0 | 0.6598 |
<!-- 09b48b6 write split sync_calls=101 sync=0.052 floor_samesize=0.136 -->
<!-- 09b48b6 query split unf=0.272 fil=0.183 floor_samesize=0.230 floor_ping=0.179 -->
| 09b48b6 | 3 | 0.294 | 5.6 | 10.5 | 0.456 | 5.6 | 10.7 | 1.000/1.000 | 0/0 | 0.6702 |
