本轮完整报告为 [ROUND5.md](../../ROUND5.md)，基线固定 `fb6cd8b81f07a537d99f6dd34fc34c29b1ff9835`，分支 `perf/exact-round5`。保留尾循环对齐 `1cdd47d` 与 INSERT scratch `50a7f86`；128D 预载候选未保留。未推送或正式提测，4000–5100 粗估不变。

WSL 原件与候选二进制位于 `/tmp/seekdb-round5`。本目录只归档小型结果；原始及私有大 fixture、二进制均不在仓库中。每组普通性能实验含五轮、两个版本、write/query 两阶段，共 20 条 JSONL；每组对应的 `*-run.json` 记录命令、成功退出及驱动耗时，分析器检查完整交错、全部独立真值及响应哈希后输出逐轮 E。普通 E 不等于正式分数。

| 文件组 | 内容与结论 |
| --- | --- |
| `preload-*.jsonl`, `preload-summary.json` | 8 组五轮，候选 123,000 条独立 TopK 真值；含默认加长、禁用 AVX-VNNI、离群及常量 ties。默认短/长查询约慢 7.3%/1.7%，撤下。 |
| `scratch-10k/100k/tail127/dim1024/outlier/constant.jsonl`, `scratch-summary.json` | 初始 scratch 独立 6 组五轮，33,000 条真值；1024D 写入改善，但 127D 查询五轮均退步，没有直接保留这一构建。 |
| `align-*.jsonl`, `align-summary.json` | 只含尾循环对齐的独立 4 组五轮，28,000 条真值；对原基线收益混合。 |
| `final-10k/10k-long/100k/tail127/dim1024/fallback128/outlier/constant.jsonl`, `final-summary.json` | 最终组合对 base 的 8 组五轮，123,000 条真值。四个常规短对照 E 改善；默认加长、常量和离群组 E 退步，全部保留。 |
| `alignment-on-scratch-tail127.jsonl`, `scratch-on-align-10k.jsonl`, `scratch-on-align-100k.jsonl`, `confirmation-summary.json` | 只改变一项的 3 组五轮，最终组合再验证 33,000 条真值。对齐修复 scratch 的尾维退步；固定对齐后的 scratch 在两核心规模 E 改善，但 100k 查询略慢。 |
| `*-tests.txt`, `final-fallback-tests.txt` | 每个候选完整 Go 测试；最终与预载还运行禁用 AVX-VNNI 的 engine 测试。含独立整数点积、折叠距离、exact 边界、过滤、尾数、ties、事务和恢复，以及新增逐元素 parser/INSERT 原子性验证。 |
| `base/scratch-allocation-diagnostic.txt` | 预构造 100 行 INSERT 的累计分配诊断：1261→1162 次，约 235,369→184,676 字节。SyncNone，不是 T/B/M 或峰值 M。 |
| `preload-disassembly-*.txt`, `*-layout.txt`, `hot-loop-layout*.json` | 预载的实际 VEX/YMM 指令与基线/候选的地址布局；最终两个标量尾循环起点模 32 均为零。 |
| `final-race.txt`, `final-crash.txt`, `final-exact-edges.txt` | engine/types race、30 轮 SIGKILL 恢复（11,579 条已提交行）、独立 exact 边界通过。 |
| `final-protocol.json`, `final-protocol.txt` | 最终二进制 native JSONL 公共协议 20/20 PASS，无平台回调或正式评分。 |
| `final-capacity.jsonl`, `final-capacity-summary.json`, `final-capacity-run.json` | 550k×1024 原始数据对应日志的私有副本，2 CPU/2GiB/无 swap/fdatasync；三查询、550,008 行独立校验，OOM=0。单版本容量确认，无对照 E。全行 census 校验主键、分类和范数，不是所有向量逐元素投影。 |
| `environment.json`, `*-source-sha256.json`, `*-binary-sha256.json`, `source-check.json`, `driver-sha256.json` | 环境、源码/二进制/驱动身份、独立候选只包含指定变更、最终源码与测试快照一致。基线二进制与 ROUND4 final 相同。 |
| `preserved-review-before.json`, `preserved-review-after.json` | 最初未跟踪的 REVIEW_NEXT.md 及 review-round4 全部证据前后哈希一致，仍未跟踪。 |

最终二进制共 **156,000 条**独立 TopK 真值（123,000+33,000），与中间候选分别计数。SQL 容量验证的 3 条查询/550,008 行另计。所有 29 组普通性能对照串行执行；局部/部分汇总文件仅保留当时进度，最终结论使用上表完整 summary 文件。

容量摘要通过已有 sqlcompare 分析器验证完成标记、逐查询真值、冷页、同 FD 阶段峰值重置、无 OOM 和阶段完整性。服务阶段和为 startup+query+shutdown 的 T/B 求和与 M 最大值，排除准备、缓存驱逐和外部真值，不是完整驱动 wall clock。源 fixture size/mtime 和私有副本 SHA256 均保持不变。

候选补丁、驱动及复现说明在 [round5](../../round5/README.md)。空 stderr 不是独立通过证据；应结合对应成功退出、原始 JSONL 和验证记录阅读。
