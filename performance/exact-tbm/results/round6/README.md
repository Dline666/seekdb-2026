本目录保留 ROUND6 的完整本地实验和验证证据，包括负收益、失败后修正的测试以及耗时异常轮次。最终决定见 [ROUND6.md](../../ROUND6.md) 和 `selection.json`，实验名 `final` 只是一个未受维度限制的组合候选，不能按名字认定为最终保留版本。

- `all-comparisons.json`：严格校验全部五轮交错记录后生成的 T/B/M/E 与逐轮配对比值；对应 `*.jsonl`、`*-run.json` 和 `*.stderr.txt` 为原始输出。每组有独立参考，不用于换算正式分数。
- `truth-counts.json`：按二进制及运行时开关分别统计独立协议 TopK 真值数量。诊断、Go 测试和容量验证单独记账。
- `*-query-work.txt`：临时插桩副本的精算行数和分配诊断，不属于计分测量。
- `*-stress*`：选定版本在全新 550k×1024 数据上的写入、查询、更新、SIGKILL 与恢复。`*-capacity*` 则是旧大 fixture 私有副本上的冷页 point/order/census 检查。两者都是单版本容量验证，不计算对照 E。
- `*-tests.txt`、`*-fallback-tests.txt`、`*-race.txt`、`*-crash.txt`、`*-exact-edges.txt`、`*-protocol.json`：对应功能、指令回退、并发、恢复和协议检查。
- `source-check.json`、`*-source-sha256.json`、`*-binary-sha256.json`：源码与实际被测二进制身份；`preserved-review-*.json` 核验原有未跟踪复查材料未变。
- `candidates/`：每个候选相对同一基线的完整补丁、独立测试、设计和源码哈希。补丁已做应用及完整重建检查；各自独立应用，不能连续叠加。其生成时的 pending 状态由最终 `selection.json` 中的决定补充。

基线为 `33485b53c039a6cc3d2c4fa5b40d064365cf6eee`。用户报告约 4100 的正式成绩，其提交与分项指标尚未核实；本目录没有新的正式成绩、推送或提测。二进制和大数据库保留在本地 WSL，不进入 Git。复现方式见 [round6/README.md](../../round6/README.md)。
