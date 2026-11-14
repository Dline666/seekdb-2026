本目录为 ROUND5 的本地实验驱动。基线固定为 `fb6cd8b81f07a537d99f6dd34fc34c29b1ff9835`，没有重新比较历史版本。结果及保留决定见 [ROUND5.md](../ROUND5.md)。

`run.py` 在 WSL ArchBench 内以 root 运行，使用 `/tmp/seekdb-round3-go` 的 Ubuntu Go 1.22.2。`prepare` 必须在生产源码仍为基线时执行，会保存原复查材料的哈希、独立源码副本和二进制哈希；基线二进制必须匹配 ROUND4 最终构建。默认结果根目录为 `/tmp/seekdb-round5`，可用 `SEEKDB_ROUND5_ROOT` 指定新的空目录。原目录、数据库 fixture 和以前的证据不应删除或覆盖。

每个独立候选由基线副本加所列文件构成，`snapshot` 不会将工作区其他改动混进去。`preload-experiment.patch`、`scratch-experiment.patch` 和 `align-experiment.patch` 保存各自相对基线的生产差异；scratch 新增的引擎回归测试位于仓库 `minivectordb/internal/engine/insert_scratch_test.go`。最终组合用 scratch 的 Go 文件加 align 的汇编文件。复现被撤下的候选时，应先将对应补丁应用到单独的基线 checkout，再执行 snapshot。

执行顺序为准备、独立构建/测试、性能对照、最终验证。性能及其他 CPU 密集任务须串行，下面的命令只展示单项例子：

```sh
python3 performance/exact-tbm/round5/run.py prepare
python3 performance/exact-tbm/round5/run.py snapshot preload internal/engine/fused_avxvnni_amd64.s
python3 performance/exact-tbm/round5/run.py test preload
python3 performance/exact-tbm/round5/run.py compare preload 10k 100k tail127 dim1024 fallback128 outlier constant
python3 performance/exact-tbm/round5/run.py compare preload 10k-long
```

普通性能驱动仍是 `../tbm_bench.go`。每阶段使用新数据库进程、目录和 cgroup，限制 2 CPU、2GiB、无 swap、128 PID，显式设置 fdatasync、GOGC=20、GOMAXPROCS=2。写入 T 为预构造 JSONL 批次的协议耗时；查询 T 排除装载和一次预热，B/M 包含全部装载、预热、查询与退出写回。B 来自数据设备 io.stat，M 来自 cgroup memory.peak，不是 Go 堆。

每组都跑五轮，版本顺序在奇数轮反转。独立标量 float64 oracle 在被测 cgroup 外预先穷举距离，并按主键破平局；响应哈希一致是附加检查。`analyze.py` 要求全部 20 条 write/query 记录、完整交错顺序、成功退出、逐条真值和相同响应哈希后才输出摘要。T/B/M 用中位数；固定参考为该组 base 前三轮各指标中位数，T/B/M 权重 0.6/0.2/0.2、平滑 1ms/1MiB/1MiB、写入:查询=1:2，逐轮计算 E 后再取中位数。不同实验不合并参考，也不换算正式分数。

`alloc.py` 将 `insert_alloc.go.txt` 加到临时源码副本中，仅诊断同一 100 行 INSERT 的累计分配；使用 SyncNone，排除 SQL 构造，不属于 T/B/M。`disassemble.py` 构建带符号的等价程序检查指令位置；所有性能仍使用原始带 `-s -w` 的二进制。

`confirm.py` 保存针对短计时和 127 维退步的补测：final 对 base 的默认加长五轮、只改变对齐的 scratch→final 尾维五轮，以及保持对齐的 align→final 两个核心规模各五轮。所有原始短对照继续保留。`archive.py` 校验当前生产源码与最终测试快照相同，并检查最初未跟踪复查报告/证据的哈希完全不变，再复制小型结果文件；不会复制或清理大 fixture。

`verify.py` 对指定最终版本串行执行 race、30 轮 SIGKILL 恢复、exact 边界、公共 JSONL 协议和 550k×1024 大 fixture 的私有副本检查。大 fixture 检查沿用 `../sqlcompare` 的独立逻辑模型、冷页验证及阶段峰值重置；查询全部主键、分类和范数，不代表全量向量逐元素投影。它是单版本容量确认，不计算对照 E；startup+query+shutdown 服务阶段和不包括准备、驱逐缓存和外部真值计算。

本地编辑、构建、测试、归档及提交均不调用正式提测或推送。
