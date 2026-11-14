ROUND4 结果清单。基线为 `88434ee392cb3e1331cdbd59da08c6ed4b94f48e`，只保留本轮与该基线的对照，以及 AVX-VNNI 两种循环展开的独立对照，不重新审查历史版本。本轮目标 linux/amd64 的最终正确性及全部计划对照已完成并归档，WSL 原件位于 `/tmp/seekdb-round4-results`。SQL、首项 AVX-VNNI、128B 展开已分别本地提交为 `d3b1703`、`06cca45`、`62fe6de`。仍保持 4000–5100 粗估，未正式提测，没有达到 5500 的正式证据；未推送。

| 源文件 | 用途与当前判断 |
| --- | --- |
| `environment.json` | CPU/ISA、编译器、构建参数、2 CPU/2GiB/无 swap、fdatasync 及未提测状态。本机支持 AVX-VNNI，没有 AVX512。 |
| `binary-sha256.txt` | base、独立候选、final、sqlcompare 及向量基准驱动的二进制 SHA256；二进制本体未纳入结果归档。 |
| `profile-run.txt`, `profile-top.txt` | 探索性 CPU profile；含查询构造工作，只用于发现扫描热点，不作为正式协议的精确占比。 |
| `atomic10k.jsonl`, `atomic100k.jsonl` | 诊断计数 worker 内累加实验，各负载 5 轮交错；独立真值通过，未保留该优化。 |
| `atomic10k-summary.json`, `atomic-summary.json` | 上述小负载/合并负载汇总；本地 E 也包含写入波动，不能仅凭微小差异宣称提分。 |
| `seed10k.jsonl`, `seed100k.jsonl` | 初始 TopK 批量缩小实验，各负载 5 轮交错；独立真值通过，未保留该优化。 |
| `seed10k-summary.json`, `seed-summary.json` | 上述汇总；两种规模没有一致收益。 |
| `atomic-experiment.patch`, `seed-experiment.patch` | 未保留实验相对本轮基线的独立源码差异，供复现；不属于最终生产变更。 |
| `sql-tests.txt` | SQL 主键点查、主键顺序、LIMIT/OFFSET、事务可见性和不读日志的规划检查已通过。 |
| `sql-small5.jsonl` | 10k×128，每版本 point 9 条/order 8 条/census 1 条，各 5 轮；每版本 90 条查询独立校验通过。 |
| `sql-small5-summary.json` | 同时报告 query-only 与 startup+query+shutdown 服务阶段和 T/B/M/E；小 census 查询 M 上升 10.6%，不能省略。 |
| `sql-large-census.jsonl` | 已完成：550k×1024 最终日志私有副本，base/sql 各一轮 census、各 549,997 行独立主键/分类/范数检查通过，OOM=0；源 size/mtime 和副本 SHA256 保持不变。 |
| `sql-large-census-summary.json` | 完整性、真值、无 OOM、冷页及文件保护检查全部通过。查询 T 为 551.704s/8.989s，B 为 834,217,586,688B/4,348,690,432B，M 均接近 2GiB；标记 `single_diagnostic`，不报告稳定倍数。 |
| `sqlcompare-driver-sha256.txt` | 已归档驱动 `main.go`、分析器与 README 的摘要；源码位于 `../../sqlcompare/`。 |
| `vnni10k.jsonl`, `vnni100k.jsonl` | 首项 AVX-VNNI 单独对 base，每负载 5 轮交错；每版本合计 18,000 条独立真值均通过，相同负载响应哈希一致。 |
| `vnni10k-summary.json`, `vnni-summary.json` | 10k/100k 查询 T 分别下降约 6.5%/9.8%，B 不变，查询 M 略增；首项候选可保留，不能换算正式分数。 |
| `vnni-exact-tests.txt`, `vnni-fallback-tests.txt` | gate、原始整数点积、折叠距离数学真值及相关 exact/强制禁用回退检查通过。 |
| `vnni-disassembly.txt`, `vnni-binary-sha256.txt` | 确认 VEX/ymm `VPDPBUSD` 编码及对应独立候选二进制摘要。 |
| `vnni-build.txt`, `vnni-build-tests.txt` | 编译器/构建辅助输出；后者为空，不能单独当作正确性通过证据。 |
| `wide10k.jsonl`, `wide100k.jsonl`, `wide10k-summary.json`, `wide-summary.json` | 每负载 5 轮 `vnni`/`vnniwide` 独立交错，各版本合计 18,000 条真值通过。10k 查询约持平、E 小降；100k 查询约降 3.0%、配对五轮均改善，M 增约 0.445MiB、E 小升。决定保留，但不替代最终组合回归。 |
| `vnniwide-exact-tests.txt`, `vnniwide-disassembly.txt`, `vnniwide-binary-sha256.txt` | 展开候选的独立原始数学/exact 检查、实际反汇编与二进制摘要。 |
| `vnniwide-build.txt`, `vnniwide-build-tests.txt` | 展开候选编译器/辅助构建输出；空输出不单独充当通过证据。 |
| `vnni-candidate.patch`, `vnniwide-candidate.patch` | 对应单项候选的源码差异，供复现独立构建；最终合并版本的验证单独记录。 |
| `protocol-docker.json` | 最终组合镜像的公共协议检查 20/20 PASS，本地诊断，无正式评分或平台回调。 |
| `docker-image-inspect.json`, `docker-binary-sha256.txt` | 镜像 `seekdb-exact:round4` 元数据；实际镜像与 final 二进制 SHA256 均为 `f45727b61cda9da39dacaa147c3aa57ac9c0d5f4e5cee86d1d5fe2f6ed4e30e0`。 |
| `docker-run-metadata.json`, `docker-build.log`, `docker-daemon.log`, `protocol-docker-run.txt` | 最终镜像构建、专用 Docker daemon 及协议运行辅助记录；以协议结果 JSON 和实际二进制摘要作为通过证据。 |
| `final-tests.txt`, `final-race.txt` | 最终组合完整 Go 测试和 engine race 通过，后者耗时 74.784 秒；目标 linux/amd64。 |
| `final-crash.txt` | 最终组合崩溃恢复压力检查通过：30 轮、5778 条已提交行。 |
| `final-exact-edges.jsonl` | 最小 BIGINT 主键、大常数向量的精确零距离及主键破平局，含过滤/预过滤开关，8 条结果均符合预期。首条为误差展开的诊断计算。 |
| `source-check.json` | 69 个 Go/汇编/go.mod 文件的最终快照与仓库一致，仅规范化 CRLF 为 LF，含 final 二进制 SHA256。 |
| `arm64-base-build.txt`, `arm64-final-build.txt` | 两版 ARM64 交叉构建均因既有 VNNI stub 缺失失败，非目标平台限制仍在；不能称为跨平台全过。 |
| `final10k.jsonl`, `final100k.jsonl`, `final10k-summary.json`, `final-main-summary.json` | 最终组合对 base，两规模各五轮，每版本合计 18,000 条独立真值通过。查询 T 下降 6.6%/10.1%、E 均改善；100k 写入 T +1.18%、查询 M +0.543MiB，均计入综合评价。 |
| `final-outlier.jsonl`, `final-tail127.jsonl`, `final-dim1024.jsonl`, `final-summary.json` | 补充三组各三轮完成；五组基线/final 各 21,000 条独立 TopK 真值通过。离群与 127 维 E 改善；1024 维查询中位数 −10.8%，但综合 E 0.500494→0.498290 略退，完整保留波动记录。 |
| `final-dim1024-long.jsonl`, `final-dim1024-long-summary.json` | 10k×1024、每轮 1000 查询的五轮补测，两版各 5000 条真值通过；查询中位数 −8.7%、M +0.102MiB、E 0.510982→0.519516。写入波动不归因于查询核，不混入每轮 200 查询三轮。 |
| `final-fallback100k.jsonl`, `final-fallback-summary.json` | 强制禁用 AVX-VNNI 的三轮对照，两版各 900 条真值通过；查询中位数 +0.32%，基本持平。E 小升主要来自写入波动，不宣称其他 CPU 提速。 |
| `final-sql-small5.jsonl`, `final-sql-small5-summary.json` | 最终组合 SQL 三组各五轮，两版各 90 查询/50,300 行通过。查询及服务阶段和 E 均改善，census 查询 M +1.492MiB（8.9%）仍保留。 |
| `final-sql-large.jsonl`, `final-sql-large-summary.json` | 大 fixture 的最终单版本容量/正确性确认，3 查询/550,008 行通过，OOM=0，冷页及文件保护检查通过。只有 final，没有对照基线或 E；原始 point/order 的 comparison 标签不代表存在同期对照。 |

SQL 对照分析器为 `../../sqlcompare/analyze.py`，要求最终 success、query_validation、同 SQL 哈希、零真值错误、零 OOM、查询前冷页和逐阶段峰值重置。最终大 fixture 单版本摘要直接提取原始阶段指标和保护检查，不用缺少 base 的数据生成对照。服务阶段和仅累计启动、查询、退出 T/B 并取阶段最大 M，排除 fixture 准备、缓存驱逐与外部真值工作，不是完整驱动 wall clock。SQL 阶段 E 不能当作普通写入/查询综合 E 或正式平台分数。

最终候选与 base 各有 26,900 条独立 TopK 真值：核心五组 21,000、禁用路径 900、1024 维加长 5,000，错误为零且逐负载响应哈希一致。最终小 SQL 每版 90 查询/50,300 行，最终大 SQL 3 查询/550,008 行，独立校验及 OOM 检查通过；这些数量分别记账，不把 SQL 范数检查当作 TopK 或全部向量逐元素检查。

全行 census 校验主键、分类和独立向量范数。SQL 优化的主要重复比较证据为小规模五轮交错，大表一对仅用于本次严重 I/O 放大的诊断。全部原始 JSONL、失败记录和辅助日志均保留，包括不支持普遍提速的实验及既有 ARM64 编译限制。原始及私有大 fixture 均保留，没有在归档时删除或复制到仓库。
