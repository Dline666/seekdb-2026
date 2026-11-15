本目录保存 ROUND6 的本地实验驱动。固定基线是 `33485b53c039a6cc3d2c4fa5b40d064365cf6eee`；不重跑历史版本，不提交平台评测。最终保留决定及全部负收益实验见 [ROUND6.md](../ROUND6.md)。用户报告的最新平台成绩约 4100，其对应提交、CPU 和分项 T/B/M 尚未核实，本地 E 不换算平台分数。

在 WSL ArchBench 中以 root 执行，使用 `/tmp/seekdb-round3-go` 的 Ubuntu Go 1.22.2。`run.py prepare` 必须在基线源码上执行，建立全新的 `/tmp/seekdb-round6`，并核验基线二进制 SHA256。可用 `SEEKDB_ROUND6_ROOT` 指定其他空目录。所有 CPU 密集测试、真值计算、性能测量都串行；不要同时运行多个驱动。

独立候选基于同一 base 快照构建。归档的 `results/round6/candidates/*.patch` 包括新增测试，可应用到基线的单独 checkout；每个补丁都以该基线为起点，不能依次叠加。`snapshot` 只复制明确列出的变更文件；`build` 保存完整源码和二进制哈希。不要将未验证的工作区文件混入候选。

```sh
python3 performance/exact-tbm/round6/run.py prepare
# 在隔离 checkout 应用某个独立候选补丁后，列出其全部变更/新增文件：
python3 performance/exact-tbm/round6/run.py snapshot heap internal/engine/topk.go internal/engine/typed_topk_test.go
python3 performance/exact-tbm/round6/run.py test heap
python3 performance/exact-tbm/round6/run.py compare heap default 10k-long 100k tail127 dim1024 fallback128 seed29 outlier constant
```

普通性能驱动是 `../tbm_bench.go`：每阶段新数据库、目录和 cgroup，2 CPU、2GiB、无 swap、128 PID，fdatasync、GOGC=20、GOMAXPROCS=2。五轮正反交错；每条 TopK 使用 cgroup 外独立标量 float64 穷举及主键破平局校验。查询 T 排除装载和一次统一预热，B/M 包含装载、预热、测量和退出写回；B 来自 io.stat，M 来自 memory.peak。驱动 wall time 包括外部真值等，不等于计分 T。

`analyze.py` 要求全部 20 条记录、完整交错顺序、成功退出、逐条真值正确及相同响应哈希，才输出结果。T/B/M 取五轮中位数；以该组第一个版本前三轮指标中位数为固定参考，按 T:B:M=0.6:0.2:0.2、写入:查询=1:2、1ms/1MiB/1MiB 平滑计算每轮 E，再取中位数。不同组的参考不可混用，坏轮不能删除。`MVDB_NO_BOOTSTRAP=1` 消融使用同一个组合二进制，仅改变运行时开关。

`diagnose.py NAME` 在临时副本给精算 kernel 加计数，报告每条查询精算行数、分配次数和累计分配字节；另用 200 条独立标量真值检查。诊断使用独立测试二进制，不属于性能 T/B/M 或最终协议真值数量。

`verify.py NAME` 串行执行 race、30 轮 SIGKILL 恢复、exact 边界、20 条公开 JSONL 协议和旧 550k×1024 大 fixture 的私有副本查询。它核验冷页、原文件不变、阶段峰值和 OOM，但不包含重新写入大数据库。

`stress_verify.py NAME` 另外建立全新 550k×1024 数据库，完成超过 2GiB 原始向量的写入、查询、更新、事务回滚、四次 SIGKILL 和恢复、两次全表 census。它要求阶段完整、每阶段峰值重置成功、73 条 TopK 真值、两次 549,997 行 census、未提交尾部截断、恢复前 ready 未发送、OOM 为零。驱动不设置请求截止时间，因此通过不能代表平台时间限制内完成。census 核验主键、分类和独立重建范数，不是全量向量逐元素投影。容量验证是单版本检查，不编造更新/恢复权重或对照 E。

日志和数据目录独占创建。失败/中断后应使用新的标签保留原始证据，不覆盖原日志，也不删除已有大 fixture。`archive_candidates.py` 归档候选补丁和设计；最终归档还应核对工作区源码与保留快照一致、原 `REVIEW_NEXT.md` 及 `results/review-round4/` 哈希未变。二进制和大数据库不提交 Git。

本轮最终保留 `heap`。完整性能比较结束后，按顺序运行下列验证；`verify.py` 和 `stress_verify.py` 不能并行。工作区与保留快照一致、`selection.json` 记录本地提交后，`archive.py heap` 校验全部原始比较和验证产物并归档。`report.py` 从这些产物生成报告草稿，不推测缺失结果。

```sh
python3 performance/exact-tbm/round6/run.py fallback heap
python3 performance/exact-tbm/round6/stress_verify.py heap
python3 performance/exact-tbm/round6/verify.py heap
python3 performance/exact-tbm/round6/archive.py heap
python3 performance/exact-tbm/round6/report.py
```

`archive_candidates.py` 需要 Git。本机 WSL 未安装 Git，实际使用 Windows Python/Git，并通过 `--source-root`、`--residual-design`、`--bootstrap-design` 传入 `\\wsl.localhost\ArchBench\tmp\...` 的 UNC 路径；它只在临时目录应用补丁，核验完整重建，不改工作区。输出中的初始 pending 状态由最终 `selection.json` 决定补充。
