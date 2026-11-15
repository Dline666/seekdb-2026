ROUND7 的本地实验入口。基线为 `4a257014c9f43fd1dbb86b008d32cd93303b4d94`，
实验目录为 WSL `/tmp/seekdb-round7`。所有构建、测试、性能和压力负载由主代理
串行调度，禁止在同一主机上并行跑数据库对照。

`run.py` 固定构建参数、源码/二进制哈希和五轮 AB/BA 普通写入/查询对照；
`analyze.py` 验证完整交错次序、独立真值数量与响应一致性后报告 T/B/M。
普通驱动的查询 T 排除装载及首条预热，B/M 包含整个进程，因此建索引的
完整服务成本须结合下面的大数据对照，不能只看普通查询 T。

`large_run.py` 调用 [large](large/README.md) 的五轮大数据查询对照，逐阶段
记录启动、冷批、同进程重放和退出。所有轮次复用一份私有日志副本；每次
启动前和冷批前都验证冷页。服务合计包括首次派生索引构建。默认原始向量
超过 2GiB，数据库限制为 2GiB、2 CPU、无 swap、fdatasync。

`update_run.py` 调用 [update](update/README.md)，在小数据、4MiB 派生预算下
比较建索引后的更新、查询和重启成本。`large_update_run.py` 调用
[large_update](large_update/README.md)，补充超过 2GiB 数据的实际更新 T/B/M；
其私有日志创建和复位均进入数据库同一 cgroup，防止写回收费遗漏。

`verify.py` 为选定二进制运行 race、崩溃、数值边界、协议及旧大日志容量检查。
目录冲突等驱动失败必须保留原记录，可用 `--capacity-only --capacity-label 新标签`
单独重跑容量阶段；`report.py --capacity-label` 指向该成功标签。
`stress_verify.py` 重新写入 550,000×1024 向量，再完成查询、更新、事务与
四次 SIGKILL 恢复；这是独立容量/正确性验证，不是性能对照或正式时限证明。
中断重跑使用 `stress_verify.py partial-balanced --stress-label 新标签`，
报告使用同一个 `--stress-label`。失败原始工件归档到 `failed-attempts/`，
并以 `stress-environment-failure.json` 记录故障与恢复过程；不覆盖失败记录。

候选在 WSL 隔离源码副本中构建，最后只把被验证的版本复制回工作树。
每次运行使用新标签，保留失败记录和负收益轮，不覆盖或筛除已有结果。
源码补丁需要显式 `git add -f`，因为仓库忽略 `*.patch`。

本地 E 仅用于同组 T/B/M 比较。用户报告的正式 4100 分尚缺对应提交、平台
CPU 和分项指标，不能把本地 E 换算成平台分数。实验脚本不执行推送或正式提测；
用户已追加授权，本轮全部验证、归档和提交完成后，向当前仓库及其个人 fork
推送同一个 `perf/exact-round7` 分支。报告通过 `--publish-target` 记录授权目标。
