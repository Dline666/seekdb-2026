本轮从 `88434ee392cb3e1331cdbd59da08c6ed4b94f48e` 继续优化 exact，仅比较本轮候选与这一基线，并单独比较 AVX-VNNI 的循环展开方式。保留 SQL 主键查询/排序与 AVX-VNNI 128B 展开，所有本轮目标 linux/amd64 的正确性、恢复及交错性能验证已完成。最终组合的两个核心向量负载查询 T 下降约 6.6%/10.1%，综合 T/B/M 指标改善；SQL 大表读取放大得到缓解。报告同时保留 census 内存上升、1024 维短对照的综合指标轻微退步及禁用 AVX-VNNI 后查询基本持平的结果。仍保持 **4000–5100 的粗估**，没有接近或达到 5500 的正式证据。未推送、未消耗正式提测次数。

原始记录与汇总已归档到 [results/round4](results/round4/README.md)，WSL 原件位于 `/tmp/seekdb-round4-results`。`base` 对应上述基线；`atomic`、`seed`、`sql` 是分别应用单项变更的独立构建。SQL 驱动、分析器及说明的摘要见 [sqlcompare-driver-sha256.txt](results/round4/sqlcompare-driver-sha256.txt)。三项生产变更已本地提交：SQL `d3b1703`、首项 AVX-VNNI `06cca45`、128B 展开 `62fe6de`。最终组合快照为 `/tmp/seekdb-round4-final/minivectordb`，各候选、最终二进制和基准驱动摘要见 [binary-sha256.txt](results/round4/binary-sha256.txt)。所有数据库性能负载均串行执行，归档未复制二进制或大 fixture。

本轮环境为 Intel Core Ultra 5 125H、WSL Linux 6.18.58.1、ext4 数据设备。统一构建为 Ubuntu Go 1.22.2、CGO 关闭、Linux amd64、GOAMD64=v1，使用 `-trimpath -ldflags='-s -w'`。数据库进程限制为 2 CPU、2GiB、无 swap、128 PID，COMMIT 保持 fdatasync；数据生成与独立真值运行在 cgroup 外，同一时间只执行一个性能工作负载。环境详情见 [environment.json](results/round4/environment.json)。本机支持 `avx_vnni`，没有 AVX512；正式平台 CPU 特性未知，因此即使本机 AVX-VNNI 实测成功，也不能据此提高 5500 达成的置信度。

探索性 `BenchmarkQueryProfile` 显示量化扫描是值得继续调查的热点。该基准还包含查询字符串构造等工作，`profile-top.txt` 的采样比例不能解释为正式 JSONL 协议查询的精确 CPU 占比；它只用于选择实验方向。

两项已测但未保留的实验为：`atomic` 将每批扫描的诊断原子计数改为 worker 内累加后集中发布；`seed` 在 TopK 堆未填满时缩小首批扫描到剩余名额，以尽早得到有限阈值。每项均在 10k×128、3000 查询和 100k×256、600 查询上进行 5 轮正反交错，并独立穷举校验该候选合计 18,000 条查询。两项真值错误均为零，同负载响应哈希与基线相同。

| 实验 | 负载 | 版本 | 查询 T（秒） | 查询 M（MiB） | 每阶段 B（MiB） | 写入/查询综合本地 E |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| atomic | 10k×128 | base | 0.326267 | 12.500 | 5.582 | 0.497985 |
| 同组 | 同上 | atomic | 0.326214 | 12.621 | 5.582 | 0.501845 |
| atomic | 100k×256 | base | 0.582712 | 148.547 | 104.629 | 0.493290 |
| 同组 | 同上 | atomic | 0.575074 | 148.602 | 104.629 | 0.498707 |
| seed | 10k×128 | base | 0.321765 | 12.250 | 5.582 | 0.499150 |
| 同组 | 同上 | seed | 0.322562 | 12.402 | 5.582 | 0.500318 |
| seed | 100k×256 | base | 0.578620 | 148.711 | 104.629 | 0.500127 |
| 同组 | 同上 | seed | 0.575096 | 148.582 | 104.629 | 0.499895 |

各指标为中位数。完整写入 T/B/M 及逐轮 E 在 `atomic-summary.json`、`seed-summary.json`；原始数据为 `atomic10k.jsonl`、`atomic100k.jsonl`、`seed10k.jsonl`、`seed100k.jsonl`。atomic 的查询变化很小，综合 E 的变化还包含本不应由查询计数改动直接改善的写入波动；seed 在两个规模上也没有一致收益。本轮未将这两项加入保留候选，原始结果继续保留，不把微小中位数差异当作确定的提分。

`sql` 候选处理通用 SELECT 的两个具体问题。主键等值查询原先会进入通用扫描，现在直接在事务可见行表中定位最多一行，并继续检查全部 WHERE 条件；输出与排序表达式仍先完成合法性验证。无过滤的主键排序及默认主键顺序现在只在主键/日志偏移元数据上选取和排序，再读取结果行，避免为排序遍历所有日志行头。有限 LIMIT/OFFSET 使用有界堆。升降序、主键极值、别名和序号排序、事务覆盖的局部测试已通过，记录在 `sql-tests.txt`；最终组合完整 Go 测试、engine race 和 Docker 协议验证均通过。

独立驱动为 [sqlcompare/main.go](sqlcompare/main.go)，测量定义见 [README](sqlcompare/README.md)，分析器为 [analyze.py](sqlcompare/analyze.py)。小规模 fixture 直接编码已知的最终逻辑模型：10,000 个原始 ID、128 维、3 个已删除 ID，其余分类均为 c9，指定 ID 使用独立生成器的更新向量。它不重放大 fixture 的更新历史。每版本每轮执行 9 条主键点查、8 条有限排序/分页及 1 条全行 census，各组均完成 **5 轮交错**；每版本合计 **90 条查询**通过独立真值。

点查以独立重建的完整预期向量作为查询向量，要求距离恰好为零，并检查缺失行及残余过滤条件。排序分页和 census 逐行验证主键、分类和独立标量 float64 范数；census 不代表逐元素向量校验。分析器同时要求完成标记、每组独立 `query_validation` 记录、所有版本/轮次相同 SQL 哈希、无 OOM、查询前冷页验证，以及逐阶段重置 memory.peak。

以下 SQL 表均为每组查询合计的中位数，T 为秒，B/M 为 MiB。point 每轮 9 条、order 每轮 8 条、census 每轮 1 条全行结果，不能直接跨组比较每条查询成本。

| SQL 组 | 版本 | 查询 T | 查询 B | 查询 M | SQL 查询阶段 E |
| --- | --- | ---: | ---: | ---: | ---: |
| point | base | 0.006511 | 5.191 | 22.965 | 0.492874 |
| point | sql | 0.003114 | 5.191 | 20.480 | 0.586187 |
| order | base | 0.020944 | 5.191 | 19.727 | 0.500158 |
| order | sql | 0.011300 | 5.191 | 19.656 | 0.588275 |
| census | base | 0.017385 | 5.191 | 22.266 | 0.496841 |
| census | sql | 0.013760 | 5.191 | 24.621 | 0.530949 |

point、order、census 的查询 T 分别下降约 52.2%、46.0%、20.9%；B 在这组实验中均相同。**小规模 census 的 M 上升 10.6%，约增加 2.355MiB**，必须与耗时改善一起评价；目前不能根据这组指标断言全部规模的查询内存都会降低。原始与汇总记录为 `sql-small5.jsonl`、`sql-small5-summary.json`。

| SQL 组 | 版本 | 服务阶段和 T | 服务阶段和 B | 服务阶段最大 M | 服务阶段和 E |
| --- | --- | ---: | ---: | ---: | ---: |
| point | base | 0.013951 | 10.383 | 23.691 | 0.491156 |
| point | sql | 0.010908 | 10.383 | 22.789 | 0.532689 |
| order | base | 0.027861 | 10.383 | 21.773 | 0.501055 |
| order | sql | 0.018416 | 10.383 | 19.656 | 0.567999 |
| census | base | 0.024418 | 10.383 | 22.266 | 0.496112 |
| census | sql | 0.021064 | 10.383 | 24.621 | 0.521397 |

这里的“服务阶段和”严格指 **startup + query + shutdown**：同一轮 T/B 求和、M 取三个阶段的最大值，再对各轮取中位数。它排除 fixture 准备、冷缓存驱逐和外部真值生成/验证，**不是完整驱动 wall clock**。启动 T 从 exec 之前开始；查询 T 包含 JSONL 请求、往返和输出，独立真值与响应解码在计时外，B/M 在校验前完成快照。查询开始前还会驱逐恢复阶段刚读过的日志页，所以服务阶段和包含人为设置的冷查询，不能等同于普通启动后直接查询的生命周期耗时。

SQL 对照只让数据库打开私有 fixture 副本；对源文件检查 size/mtime，对副本在前后检查 SHA256，并在每次只读试验之后检查 size/mtime。每次启动前及 ready 后查询前，对私有日志执行 Sync、FADV_DONTNEED，再用 mincore 确认至多一页尾页驻留；没有执行系统级 drop_caches。数据库跨重启复用 cgroup，memory.peak 用同一打开的描述符重置和读取，另以 50ms 采样 anon/file/RSS。M 包含页缓存和内核内存，不等于 Go 堆；不同时间的 anon/file 采样峰值不能相加。

大数据 SQL 诊断已完成：对 ROUND3 最终日志的私有副本运行 `base`、`sql` 各一轮全行 census。源 fixture 原始向量为 550,000×1024 float32，即 2,252,800,000B（2.098GiB），已有 549,997 行存活数据、分类全为 c9，日志为 4,313,968,692B。该场景保留真实更新历史；两版各自全部 549,997 行均通过独立主键、分类、范数校验，总计 1,099,994 行，`oom=0`、`oom_kill=0`。

| 大 census 单轮诊断 | 版本 | T（秒） | B（MiB） | M（MiB） |
| --- | --- | ---: | ---: | ---: |
| 查询阶段 | base | 551.704022 | 795571.887 | 2048.004 |
| 查询阶段 | sql | 8.989106 | 4147.234 | 2048.000 |
| startup+query+shutdown 服务阶段和 | base | 554.225040 | 799686.012 | 2048.004 |
| startup+query+shutdown 服务阶段和 | sql | 11.090084 | 8261.359 | 2048.000 |

查询阶段物理 B 从本次基线的 834,217,586,688B（776.926GiB）降至候选的 4,348,690,432B（4.050GiB）；M 仍接近 2GiB 限制。结果与先在主键/偏移元数据上排序、再读取输出行的访问方式相符，证明这次大数据路径没有继续出现基线的严重读取放大。**这是同期单轮诊断，不是稳定倍数的估计；该优化的主要重复比较证据仍是前述小规模五轮交错。** 未增加多轮大型点查/分页测量，也不把单次大 census 外推为所有 SQL 工作负载的收益。

完整原始记录和分析分别见 [sql-large-census.jsonl](results/round4/sql-large-census.jsonl)、[sql-large-census-summary.json](results/round4/sql-large-census-summary.json)，分析器明确标记 `single_diagnostic=true`。源文件 size/mtime 保持不变，私有副本前后 SHA256 均为 `b73ddf01090e76bc33a094e0d018d3d6b08de4c55ed4f41047206e2d9f9654d2`，每次查询前的冷页检查通过；fixture 均保留。单轮阶段 E 只描述该次观测，不用于正式评分换算。

AVX-VNNI 首项独立候选已完成五轮交错。它在支持的 CPU 上使用 VEX 编码的 256 位 `VPDPBUSD` 整数点积，保留原有保守误差界、候选精确距离复算和主键破平局。CPUID/XCR0 检测决定是否进入此路径，并支持环境变量禁用后回退。当前保留候选名为 `vnni`；其二进制摘要为 `b5e2851441e6ed37da7ef81db7cd805f1d128fc0ff3d59bf0e7532e2964a531b`，见 [vnni-binary-sha256.txt](results/round4/vnni-binary-sha256.txt)。下面仍是单项变更对基线的对照，不是最终 SQL+AVX-VNNI 合并版结果。

查询量化码先减去 128 转为有符号字节，同时以 `qminAdj = qmin + 128*qscale` 吸收偏移；整数点积在当前支持维度内不会溢出 int32。原浮点安全余量使用 `Q = |qmin| + 256*qscale`，仍覆盖 `|qminAdj|` 及偏置后的项，因此保留原有保守 slack，不收紧误差界。通过下界筛选的候选继续用原始向量计算精确距离。

| 负载 | 版本 | 写入 T（秒） | 查询 T（秒） | 写入 M（MiB） | 查询 M（MiB） | 每阶段 B（MiB） | 写入/查询综合本地 E |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 10k×128，3000 查询 | base | 0.206007 | 0.316798 | 9.613 | 12.504 | 5.582 | 0.499769 |
| 同上 | vnni | 0.193608 | 0.296069 | 9.484 | 12.645 | 5.582 | 0.511948 |
| 100k×256，600 查询 | base | 2.723751 | 0.573882 | 111.055 | 148.766 | 104.629 | 0.499596 |
| 同上 | vnni | 2.731886 | 0.517489 | 111.254 | 148.773 | 104.629 | 0.510022 |

该独立候选查询 T 分别下降约 **6.5% 和 9.8%**，B 不变，查询 M 分别增加约 0.141MiB、0.008MiB。写入项也照常计入 E，不能把它的波动归因于整数查询核。基线与候选各完成 **18,000 条独立穷举真值校验**，错误为零，同负载响应哈希相同。原始记录为 [vnni10k.jsonl](results/round4/vnni10k.jsonl)、[vnni100k.jsonl](results/round4/vnni100k.jsonl)，汇总为 [vnni-summary.json](results/round4/vnni-summary.json)。普通向量对照每个阶段使用新进程、新数据目录；查询 T 排除首条预热，预热时间单列，B/M 覆盖载入、预热、测量及退出写回。

独立正确性记录 [vnni-exact-tests.txt](results/round4/vnni-exact-tests.txt) 包含 ISA gate、环境禁用、原始整数点积、折叠距离的独立标量数学校验，以及不同维数/尾数、正负极值、过滤、离群、更新/删除/事务/恢复和线程路径；该组已通过。强制禁用后的回退用例见 [vnni-fallback-tests.txt](results/round4/vnni-fallback-tests.txt)。[反汇编](results/round4/vnni-disassembly.txt) 已确认使用 VEX/ymm 指令编码。上述证据支持保留 AVX-VNNI 首项候选；收益依赖本机 CPU 特性，不外推至未知正式平台。

128B 循环展开的独立候选 `vnniwide` 已完成与首项 `vnni` 的五轮交错，并决定保留。此处 E 的固定参考是 **vnni 的前三轮**，不能把它的绝对数值与前表参考 base 的 E 直接比较。两个候选在这组实验中也各完成 18,000 条独立真值，错误为零、响应哈希相同。

| 负载 | 版本 | 写入 T（秒） | 查询 T（秒） | 写入 M（MiB） | 查询 M（MiB） | 每阶段 B（MiB） | 综合本地 E（参考 vnni） |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 10k×128，3000 查询 | vnni | 0.203350 | 0.297390 | 9.629 | 12.586 | 5.582 | 0.499743 |
| 同上 | vnniwide | 0.207261 | 0.296820 | 9.805 | 12.156 | 5.582 | 0.498961 |
| 100k×256，600 查询 | vnni | 2.786600 | 0.517276 | 111.242 | 148.516 | 104.629 | 0.500215 |
| 同上 | vnniwide | 2.826465 | 0.501867 | 111.281 | 148.961 | 104.629 | 0.502433 |

10k 的查询 T 约持平，综合 E 从 0.499743 小降至 0.498961；不能把 128B 展开描述为两个规模都改善。100k 查询 T 下降约 **3.0%**，五轮配对的 `T(vnniwide)/T(vnni)` 分别为 0.97246、0.97682、0.98081、0.97066、0.96258，均小于 1；B 不变，查询 M 增加约 **0.445MiB**，综合 E 从 0.500215 升至 0.502433。这是在较大查询负载上的小幅收益与内存/写入波动之间的取舍，尚不能代表其他维数、CPU 或负载的普遍结果。

原始记录见 [wide10k.jsonl](results/round4/wide10k.jsonl)、[wide100k.jsonl](results/round4/wide100k.jsonl)，汇总为 [wide-summary.json](results/round4/wide-summary.json)。[vnniwide-exact-tests.txt](results/round4/vnniwide-exact-tests.txt) 的整数点积、折叠距离、exact 及 mutation/边界检查通过；[反汇编](results/round4/vnniwide-disassembly.txt) 保留展开后的实际指令。该独立候选二进制 SHA256 为 `2b0e262495bd00684c7501877a2d0f18dc92553fa86aaf6d3989db20b3ccb7ba`。独立候选差异分别保存在 [vnni-candidate.patch](results/round4/vnni-candidate.patch)、[vnniwide-candidate.patch](results/round4/vnniwide-candidate.patch)。这些单项结果与下面最终 SQL+AVX-VNNI+128B 展开组合结果分别归档。

最终组合 `final` 的两个核心规模已完成各五轮与本轮基线的交错对照。这部分基线和 final 各完成 18,000 条独立真值检查，错误为零，同负载响应哈希一致。各指标为中位数：

| 最终组合负载 | 版本 | 写入 T（秒） | 查询 T（秒） | 写入 M（MiB） | 查询 M（MiB） | 每阶段 B（MiB） | 写入/查询综合本地 E |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 10k×128，3000 查询 | base | 0.206409 | 0.318625 | 9.730 | 12.414 | 5.582 | 0.501375 |
| 同上 | final | 0.202361 | 0.297684 | 9.719 | 12.590 | 5.582 | 0.507287 |
| 100k×256，600 查询 | base | 2.843404 | 0.576423 | 111.039 | 148.500 | 104.629 | 0.507559 |
| 同上 | final | 2.876942 | 0.518490 | 111.129 | 149.043 | 104.629 | 0.519012 |

最终组合的查询 T 分别下降约 **6.6% 和 10.1%**，两项综合 E 均改善；B 中位数不变。10k 查询 M 增加约 0.176MiB；100k 写入 T 上升 **1.18%**、查询 M 增加约 **0.543MiB**，这些成本已计入综合 E。原始记录为 [final10k.jsonl](results/round4/final10k.jsonl)、[final100k.jsonl](results/round4/final100k.jsonl)，汇总为 [final-main-summary.json](results/round4/final-main-summary.json)。本表是完整组合的同期测量，不通过累加单项优化的百分比推导。

最终离群向量、127 维尾数和 1024 维的各三轮交错也已完成。加上上述两组，五组中基线和 final 各通过 **21,000 条独立 TopK 真值检查**（15,000+3,000+900+1,500+600），各负载响应哈希相同。这个数量只覆盖五组向量性能对照，另有禁用路径 900 条和 1024 维加长补测 5,000 条；SQL 单独记账。

| 最终补充负载 | 版本 | 写入 T（秒） | 查询 T（秒） | 写入 M（MiB） | 查询 M（MiB） | 每阶段 B（MiB） | 写入/查询综合本地 E |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 100k×256，离群，300 查询 | base | 2.853481 | 0.288172 | 111.223 | 145.238 | 104.629 | 0.499991 |
| 同上 | final | 2.850307 | 0.253965 | 111.020 | 145.031 | 104.629 | 0.510399 |
| 10k×127，500 查询 | base | 0.192928 | 0.113310 | 9.535 | 11.605 | 5.543 | 0.499958 |
| 同上 | final | 0.200574 | 0.082290 | 9.734 | 12.734 | 5.543 | 0.527744 |
| 10k×1024，200 查询 | base | 0.838483 | 0.103562 | 51.438 | 58.207 | 39.762 | 0.500494 |
| 同上 | final | 0.842882 | 0.092337 | 51.418 | 58.391 | 39.762 | 0.498290 |

离群组查询 T 下降约 11.9%，127 维组下降约 27.4%，两者综合 E 改善；127 维查询 M 增加约 1.129MiB，表中保留该成本。**1024 维的查询 T 中位数虽下降约 10.8%，综合 E 却由 0.500494 略降至 0.498290**：其中一轮 final 写入为 1.088630 秒，而其余写入约 0.84 秒；另一轮 final 查询为 0.109277 秒，对应 base 为 0.103030 秒。E 先按轮计算再取中位数，不能从各阶段中位数重新拼出一个更有利的 E，也不能概括为五组全胜。

三组原始记录为 [final-outlier.jsonl](results/round4/final-outlier.jsonl)、[final-tail127.jsonl](results/round4/final-tail127.jsonl)、[final-dim1024.jsonl](results/round4/final-dim1024.jsonl)，五组汇总为 [final-summary.json](results/round4/final-summary.json)。为进一步检查 1024 维的波动，已补充每轮 1000 查询、五轮交错，使用独立负载键和独立汇总；保留每轮 200 查询的三轮记录，两种查询数不混合。

| 1024 维加长补测 | 写入 T（秒） | 查询 T（秒） | 写入 M（MiB） | 查询 M（MiB） | 每阶段 B（MiB） | 写入/查询综合本地 E |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| base | 0.916248 | 0.500238 | 51.676 | 58.762 | 39.762 | 0.510982 |
| final | 0.810133 | 0.456739 | 51.590 | 58.863 | 39.762 | 0.519516 |

加长补测查询 T 中位数下降约 **8.7%**，查询 M 增加约 0.102MiB，B 不变，综合 E 改善。写入 T 的变化同样计入 E，但该优化没有修改写入路径，不能将这组写入波动归因于查询整数核。两版各 5,000 条独立真值通过，响应哈希一致。证据见 [final-dim1024-long.jsonl](results/round4/final-dim1024-long.jsonl)、[final-dim1024-long-summary.json](results/round4/final-dim1024-long-summary.json)；该补测支持本机加长查询负载的收益，不抹去短对照的混合结果。

强制设置 `MVDB_NO_AVXVNNI=1` 的最终候选与 base 在 100k×256、每轮 300 查询上完成三轮交错，两版各 900 条真值通过、响应哈希相同。

| AVX-VNNI 禁用对照 | 写入 T（秒） | 查询 T（秒） | 写入 M（MiB） | 查询 M（MiB） | 每阶段 B（MiB） | 写入/查询综合本地 E |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| base | 2.982055 | 0.290323 | 111.035 | 145.098 | 104.629 | 0.500348 |
| final，禁用 AVX-VNNI | 2.728411 | 0.291255 | 110.895 | 145.004 | 104.629 | 0.504270 |

禁用后的查询 T 增加约 **0.32%**，基本持平。综合 E 的小幅上升主要来自写入波动，不能据此宣称不支持 AVX-VNNI 的机器也有向量查询提速。该实验在同一 CPU 上强制走回退路径，不等价于测试所有其他 CPU。原始与汇总为 [final-fallback100k.jsonl](results/round4/final-fallback100k.jsonl)、[final-fallback-summary.json](results/round4/final-fallback-summary.json)。

最终组合还完成了与 base 的小规模 SQL 五轮交错，fixture 和 9/8/1 条 point/order/census 用例与单项 SQL 对照一致。每版本 **90 条查询、50,300 行结果**通过独立校验。下表 T 为秒、M 为 MiB；所有行的查询 B 中位数均为 **5.191MiB**，startup+query+shutdown 服务阶段和 B 均为 **10.383MiB**。

| 最终 SQL 组 | 版本 | 查询 T | 查询 M | 查询阶段 E | 服务阶段和 T | 服务阶段最大 M | 服务阶段和 E |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| point | base | 0.007313 | 15.613 | 0.494499 | 0.014395 | 18.164 | 0.493011 |
| point | final | 0.003495 | 15.898 | 0.587165 | 0.010329 | 15.898 | 0.539725 |
| order | base | 0.020471 | 16.012 | 0.500000 | 0.028016 | 16.012 | 0.500000 |
| order | final | 0.011295 | 14.965 | 0.584870 | 0.018518 | 14.965 | 0.561326 |
| census | base | 0.017499 | 16.730 | 0.495409 | 0.024837 | 16.730 | 0.496198 |
| census | final | 0.013941 | 18.223 | 0.526011 | 0.021336 | 18.223 | 0.516525 |

最终 SQL 三组的查询阶段及服务阶段和 E 均改善，同时 point 查询 M 增加约 0.285MiB，census 查询 M 增加约 **1.492MiB（8.9%）**。它们仍是局部 SQL 指标，不能与普通向量写入/查询综合 E 混用。原始与汇总为 [final-sql-small5.jsonl](results/round4/final-sql-small5.jsonl)、[final-sql-small5-summary.json](results/round4/final-sql-small5-summary.json)。

最终组合在原始向量超过 2GiB 的大 fixture 私有副本上完成 point/order/census 各一条查询，合计 **3 条查询、550,008 行结果**通过独立校验，OOM 为零。这里只有 final 一个版本，是容量与正确性确认，没有同期基线，不计算对照 E 或稳定提速倍数。

| 最终大 fixture 单版本确认 | 查询 T（秒） | 查询 B（MiB） | 查询 M（MiB） | 服务阶段和 T（秒） | 服务阶段和 B（MiB） | 服务阶段最大 M（MiB） |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| point，1 行 | 0.003443 | 4.008 | 50.125 | 1.753743 | 4118.133 | 2048.000 |
| order，10 行 | 0.114793 | 16.000 | 62.125 | 1.887398 | 4130.125 | 2048.000 |
| census，549,997 行 | 8.692078 | 4140.059 | 2048.000 | 10.224054 | 8254.184 | 2048.000 |

恢复阶段仍须读取完整约 4.018GiB 日志，所以 point/order 查询阶段本身的较低 M 不能代表全生命周期内存。census 查询的 B 为约 4.043GiB，M 仍到达 2GiB 限制；采样显示峰值主要为文件缓存及映射页，不能把 cgroup M 当作 Go 堆。查询前驻留页均为零，逐阶段 memory.peak 重置通过，源 size/mtime 和私有副本 SHA256 保护通过。原始 [final-sql-large.jsonl](results/round4/final-sql-large.jsonl) 与 [单版本摘要](results/round4/final-sql-large-summary.json) 均保留；census 只验证主键、分类和独立范数，不称为所有向量逐元素校验。服务阶段和沿用前述定义，排除准备、驱逐缓存及外部真值工作，不是完整驱动 wall clock。

本轮沿用的本地指标为 `f(R)=R/(1+R)`，T/B/M 权重 0.6/0.2/0.2，分别以 1ms/1MiB/1MiB 平滑；固定参考取对应独立实验 `base` 前三轮各指标中位数，128B 展开实验改用 `vnni` 的前三轮，每轮分别计算 E 后再取中位数。普通向量基准再使用写入:查询=1:2；**SQL 单阶段 E 和服务阶段和 E 都不使用这一写入/查询权重，不能与普通综合 E 混用**。单轮 census 的参考只有一轮，它仅描述该次对照。无论局部阶段 E 是否改善，本报告都不换算正式平台分数。

最终组合完整 Go 测试通过，engine race 通过（74.784 秒）；崩溃恢复压力检查通过 30 轮、5778 条已提交行。边界协议诊断验证了最小 BIGINT 主键和 512 维大常数向量的精确零距离及主键破平局，包含过滤、启用与关闭预过滤路径；8 条结果均与预期一致。证据见 [final-tests.txt](results/round4/final-tests.txt)、[final-race.txt](results/round4/final-race.txt)、[final-crash.txt](results/round4/final-crash.txt)、[final-exact-edges.jsonl](results/round4/final-exact-edges.jsonl)。[source-check.json](results/round4/source-check.json) 确认 69 个 Go/汇编/go.mod 文件的最终快照与仓库相同，仅规范化 CRLF 为 LF。

最终组合镜像 `seekdb-exact:round4` 的公共协议检查 **20/20 PASS**，覆盖 SQL、向量、事务及重启/存储变更；这是本地协议诊断，没有平台回调或正式评分。镜像实际二进制与 final 构建的 SHA256 均为 `f45727b61cda9da39dacaa147c3aa57ac9c0d5f4e5cee86d1d5fe2f6ed4e30e0`。原始证据为 [protocol-docker.json](results/round4/protocol-docker.json)、[docker-image-inspect.json](results/round4/docker-image-inspect.json)、[docker-binary-sha256.txt](results/round4/docker-binary-sha256.txt)。镜像检查成功不能替代最终组合性能或独立穷举真值回归。

本轮目标平台是 linux/amd64，上述检查全部通过。额外 ARM64 交叉构建中，基线与 final 均因既有 VNNI 符号缺少对应 stub 而失败，记录在 `arm64-base-build.txt`、`arm64-final-build.txt`；这是未解决的非目标平台编译限制，不能称为跨平台全部通过。

最终候选合计完成 **26,900 条独立 TopK 真值检查**（五组 21,000 + 禁用 AVX-VNNI 900 + 1024 维加长 5,000），基线也完成相同数量；所有结果无真值错误、同负载响应哈希一致。最终 SQL 小规模为 90 条查询/50,300 行，大 fixture 为 3 条查询/550,008 行，均无真值错误与 OOM。正确性、engine race、崩溃恢复、边界检查、Docker 公共协议及全部计划对照已完成，原始记录与小型构建/运行日志已归档。下一步判断正式分数仍需目标平台证据；当前不提高 4000–5100 的粗估，也没有使用正式提测次数。
