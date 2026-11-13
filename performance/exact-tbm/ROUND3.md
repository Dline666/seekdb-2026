本轮从 `af1163209d0489fc7f0119c26d9595266dfa6d46`（ROUND2）继续优化 exact，重点是超过内存的数据、离群值和大 UPDATE。普通随机负载的综合指标接近持平；离群查询、大 UPDATE 和大日志恢复已有独立交错对照支持。当前仍没有正式提测结果，不能据此认定接近或达到 5500。未推送或消耗正式提测次数。

三个优化分别把自己的源码差异应用到同一个 af116 基线构建，再单独对照：`bounds` 仅包含分块误差界，`stream` 仅包含流式 UPDATE，`recovery` 仅包含压缩判断早退；`final` 合并三项。统一使用 Ubuntu Go 1.22.2、CGO 关闭、Linux amd64、`-trimpath -ldflags='-s -w'`。`round2` 二进制摘要与上一轮最终候选一致，各版本摘要见 [binary-sha256.txt](results/round3/binary-sha256.txt)。没有重新审查历史版本。

源码拆为三个本地提交：分块界 `219ca11`、恢复判断 `92432ad`、流式 UPDATE `2585f13`；最终生产源码对应 `2585f13d05868efa8d39b8c5e41893dbcc61f4ff`。构建与环境元数据见 [metadata.json](results/round3/metadata.json)。

本轮改动及其边界：

- 每 128 个索引槽保留 16B 的量化尺度及绝对坐标幅度上界，约 0.125B/行，计入派生索引预算。连续融合和分类融合扫描按块边界取阈值；离散候选、多遍扫描合并当前批次实际覆盖块的上界。单个离群向量由此只放宽局部批次。更新时界只增大，删除后的旧大界也只保留在局部块，重建时刷新。候选仍重新计算精确距离，并按距离、主键排序。
- 大 UPDATE 先验证全部赋值，再按语句前的日志偏移顺序逐行编码、写入同一事务，避免同时保留所有更新行及扩容产生的多代数组。固定宽度估计或实际编码超过 8MiB 时启用；显式事务继续读取此前未提交版本，所有 SET 表达式读取该行的语句前版本。正常 SQL 校验错误发生在写入前；存储失败沿用 FatalError 退出及未提交日志恢复规则。COMMIT 仍执行 fdatasync。
- 启动时以固定宽度列及 VARCHAR 最小长度编码构造存活字节下界；若垃圾字节上界已不满足压缩条件，就跳过原来的随机行头遍历。必要的完整计算、日志重写、同步及原子替换保持原语义。严格小于才早退，两个阈值的等号边界仍进入原判断。

压力定位使用 550,000 行、1024 维，原始 float32 向量共 **2,252,800,000B（2.098GiB）**，超过进程的 2GiB 限制。原基线基础流程成功后，单条全表分类 UPDATE 在约 224.566s 后 OOM；采样匿名内存最高 2032.445MiB，失败阶段物理读取约 249.644GiB、写入为零，符合仍在整批变更规划阶段保留大量行副本的代码行为。写入和查询接近 2GiB 的占用主要来自文件缓存，不能等同于 Go 堆或索引常驻数据；mmap 的 1GiB 虚拟地址扩展也不是同等物理分配。诊断、采样口径和初版驱动限制见 [MEMORY_NOTES.md](MEMORY_NOTES.md) 及 [stress-base.jsonl](results/round3/stress-base.jsonl)。这组诊断不用于最终候选的严格性能比较。

普通及离群查询对照如下。查询 K=10，一半带等值分类过滤；T 为秒，M/B 为 MiB，均列各指标中位数。普通负载每版本 5 轮，最终离群对照 3 轮，执行顺序逐轮正反交错。

| 负载 | 版本 | 写入 T | 查询 T | 写入 M | 查询 M | 每阶段 B | 本地 E |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 10k×128，1000 查询 | round2 | 0.197707 | 0.107710 | 9.734 | 12.016 | 5.582 | 0.498598 |
| 同上 | final | 0.200072 | 0.107928 | 9.758 | 12.090 | 5.582 | 0.499031 |
| 100k×256，300 查询 | round2 | 2.668515 | 0.292501 | 110.918 | 145.059 | 104.629 | 0.500414 |
| 同上 | final | 2.691948 | 0.297655 | 111.020 | 145.074 | 104.629 | 0.498031 |
| 100k×256，单离群值，300 查询 | round2 | 2.644665 | 1.352389 | 111.145 | 144.828 | 104.629 | 0.500186 |
| 同上 | final | 2.692508 | 0.290988 | 111.129 | 144.879 | 104.629 | 0.628553 |

普通负载的 E 变化为 +0.000433 和 −0.002383，不能声称普遍提速或显著提分。最终候选在离群负载中的查询 T 下降 78.5%，B 不变、M 接近；完整汇总见 [summary.json](results/round3/summary.json)。只应用分块界的独立 5 轮离群实验也得到查询 T 从 1.365326s 到 0.302898s（约 4.51 倍），查询 M 从 144.922 到 144.930MiB、B 不变，本地 E 从 0.501506 到 0.631024，见 [outlier-summary.json](results/round3/outlier-summary.json) 和 [outlier100k.jsonl](results/round3/outlier100k.jsonl)。该数据只把第一行的一个坐标改为 1e9；上表不代表全部异常分布。

大 UPDATE 和恢复分别单独按阶段评价，以下均为 3 轮交错中位数。M 仍是 cgroup 总峰值，包含文件缓存；B 是数据设备物理读写量，不是逻辑日志长度。

| 阶段 | 版本 | T（秒） | B（MiB） | M（MiB） | 阶段本地 E |
| --- | --- | ---: | ---: | ---: | ---: |
| 100k×256 全表分类 UPDATE | round2 | 0.982095 | 90.637 | 882.160 | 0.499928 |
| 同上 | stream | 0.546195 | 90.637 | 259.332 | 0.640035 |
| 550k×1024 冷启动恢复 | round2 | 47.285243 | 86869.051 | 2048.004 | 0.500351 |
| 同上 | recovery | 1.567295 | 2167.719 | 2047.918 | 0.875870 |
| 恢复＋随后 2 条查询＋退出 | round2 | 50.057042 | 88758.406 | 2048.004 | 0.499126 |
| 同上 | recovery | 4.540043 | 7775.621 | 2048.000 | 0.834193 |

流式 UPDATE 的 T 下降 44.4%、M 下降 70.6%，物理 B 相同，见 [update-summary.json](results/round3/update-summary.json)。每轮从新目录运行相同的持久化更新流程，过程中及恢复后有独立真值校验；[run_update_comparison.py](run_update_comparison.py) 记录交错规则。这组是可容纳于内存的 100k 对照；原基线的 550k OOM 诊断不纳入阶段 E。

恢复对照复用相同的 2,270,621,059B 日志及同一 cgroup，逐次对该日志 Sync、FADV_DONTNEED，并用 mincore 确认页缓存已驱逐后才启动计时；没有清理系统其他文件缓存。启动计量从创建子进程之前开始。原基线每次顺序恢复后再随机读行头，产生明显页回收和读取放大；优化后启动 B 下降 97.5%，M 仍接近限制。计入随后查询和退出，T 仍从 50.057s 降到 4.540s、B 下降 91.2%，说明收益并非只把工作移到首条查询。6 次恢复后的共 12 条查询均通过独立真值校验，日志长度保持不变，OOM 计数为零。原始记录及两种口径见 [recovery-comparison.jsonl](results/round3/recovery-comparison.jsonl)、[recovery-summary.json](results/round3/recovery-summary.json)、[recovery-total-summary.json](results/round3/recovery-total-summary.json)。

本地 E 沿用 `T:B:M=0.6:0.2:0.2`、`写入:查询=1:2`、`f(R)=R/(1+R)`，T 加 1ms、B/M 加 1MiB 平滑；固定参考为各独立实验 round2 前 3 轮指标中位数，每轮算 E 后取中位数。参考版本自身各轮 E 因采样波动不一定恰好为 0.5。UPDATE/恢复表的“阶段本地 E”只应用 T/B/M 权重，不人为给这些阶段添加正式评测权重，不能与写入/查询综合 E 混用。分析脚本为 [analyze_round3.py](analyze_round3.py) 和 [compare_stress_phase.py](compare_stress_phase.py)。

环境为 Intel Core Ultra 5 125H / AVX2、WSL ArchBench、ext4 数据设备；数据库进程独立 cgroup：2 CPU / 2GiB / 128 PID、无 swap。驱动及独立标量 float64 穷举、主键破平局计算运行在限制之外。普通查询基准每阶段新进程、新目录，查询 T 排除首条预热（时间另记），B/M 覆盖装载、预热、测量及退出写回；T 包括协议往返和输出，真值计算、响应校验在计时外。压力驱动跨重启复用 cgroup，按阶段通过同一打开的 memory.peak 文件描述符重置并读取峰值，每 50ms 采样 anon/file/RSS；采样峰值可能遗漏瞬时值，也不能把不同时间的 anon、file 最大值相加。压力驱动显式指定 fdatasync、256MiB 索引预算、GOMAXPROCS=2、GOMEMLIMIT=1288490188、GOGC=20。大规模默认预算无法容纳全部量化副本，仍会退回日志上的 exact 扫描。

已完成的正确性证据：保存的 [完整 Go 测试](results/round3/tests.txt) 和 [race 输出](results/round3/race.txt) 均通过；[随机崩溃测试](results/round3/crash.txt) 完成 30 轮、11,838 条已提交行；[协议边界](results/round3/exact-edges.jsonl) 覆盖最小 BIGINT 主键和大常量相消/零距离 ties。final 在主查询对照中独立校验 7400 条查询，另有种子 29 的 500 条、127 维尾数 200 条、512 维逆序主键常量 100 条，共 **8200 条**，均无真值错误；同负载各版本的结果哈希一致。新增单测还独立校验分块界下的更新、删除、槽复用、事务及恢复，并验证 1024 行中的单离群值最多让 256 行进入精确核。流式 UPDATE 用例覆盖后置校验失败、旧值赋值、事务及恢复，也包含未 flush 事务行和两次跨 4MiB Flush 的逐元素检查。

最终 550k×1024 全流程已通过，见 [stress-final.jsonl](results/round3/stress-final.jsonl) 和 [简化汇总](results/round3/stress-final-summary.json)。最终日志 **4,313,968,692B（4.018GiB）**，全程维持 `memory.max=2147483648`、无 swap，`oom=0`、`oom_kill=0`。内核记录的总峰值为 2048.254MiB，50ms 采样另用于区分 anon/file。完成 73 条独立穷举 TopK 校验、两次各 549,997 行的主键/分类/向量范数 census；census 不等于逐元素向量比较。四次 SIGKILL 覆盖已确认提交、未提交事务、恢复中再次被杀、全表 UPDATE 提交后被杀，最终正常退出。

未提交事务内的有真值 TopK 查询强制 Flush 后，文件明确增加 **8,285B**；恢复后日志恰好回到最后提交边界 **2,270,621,059B**，并验证被更新/删除的旧行恢复、新插入行不存在。恢复中强杀发生于 ready 之前。旧诊断中仅可能留在用户态的尾部没有被用来替代这项验证。

以下为最终容量验证中的观测值，T 为秒、B/M 为 MiB。各阶段连续使用同一数据库和 cgroup，不能与独立阶段的综合 E 混算；每组查询为 12 条合计，写入 T 排除驱动生成数据的时间。

| 阶段 | T | B | M | 采样 anon 峰值 |
| --- | ---: | ---: | ---: | ---: |
| 写入 550,000 行 | 39.464 | 2198.922 | 2048.004 | 39.660 |
| 写入后 12 条查询 | 60.047 | 8350.727 | 2048.047 | 59.727 |
| 全表分类 UPDATE | 3.922 | 3821.121 | 2048.000 | 229.402 |
| 全表 UPDATE 提交后强杀恢复 | 2.226 | 3235.078 | 2048.000 | 45.738 |
| 上述恢复后 12 条查询 | 27.719 | 43948.062 | 2048.199 | 55.434 |
| 最后全行排序 census | 585.699 | 801396.754 | 2048.227 | 249.043 |

大 UPDATE 完成了基线会 OOM 的操作；不能把“旧版失败 224.566s”作为完成同一工作的耗时来计算加速倍数。UPDATE 后全部分类变为 c9，过滤查询命中全表，选择性也不同于写入后的查询。最后的全行排序 census 虽然正确完成，却耗时 **585.7s、读取约 782.6GiB**，显示严重的 I/O 放大；通用 `scanSorted` 按 map 顺序读行头，是下一轮定位的首要路径。压力驱动没有请求超时，这不是正式时限通过的证据；下一轮应优先研究通用扫描的访问顺序，并独立衡量新增排序/偏移暂存对 M 的影响。当前不能将大数据验证概括为所有查询都已高效。

实际 Docker 镜像 `seekdb-exact:round3` 构建完成，镜像 ID 为 `sha256:f6cc9435373aba3dd4b84c00b4c4ab20d5652c0a7c76fbffdaa88de3f6ffc151`；容器在 2 CPU / 2GiB / 无 swap、只读根文件系统下通过全部 **20 个公开协议用例**。镜像内可执行文件 SHA256 为 `6535372ed1dae468838f87bbc435ec66a63be049894c7d7d08539157dd6b86c3`，与上述性能测试的 final 完全一致。见 [协议结果](results/round3/protocol-docker.json)、[构建日志](results/round3/docker-build.txt)、[镜像二进制摘要](results/round3/docker-binary-sha256.txt)。Docker 测试在大数据压力结束后才运行，避免影响测量。

本轮不更新“4000–5100”的粗估区间，也不把查询倍数、恢复阶段 E 或内存降幅换算为正式分数。普通负载仍接近持平，异常数据及生命周期阶段的收益能否体现在正式评分，取决于平台实际工作负载与分项口径；5500 继续作为目标。

本地复现先准备上述五个独立版本，统一编译为 `round2`、`bounds`、`stream`、`recovery`、`final` 放入 `/tmp/seekdb-round3-bin`。独立源码构造规则是从 af116 分别应用 `vecindex.go/prefilter.go/topk.go/eqset.go`、`dml.go`、`compact.go` 的当前差异；最终候选应用三组差异。在 cgroup v2 root 权限及空间充足的 ext4 临时目录中执行，所有数据目录使用新的独立路径：

```sh
go build -o /tmp/exact-tbm performance/exact-tbm/tbm_bench.go
go build -o /tmp/exact-stress performance/exact-tbm/stress/main.go
/tmp/exact-tbm -bin-dir /tmp/seekdb-round3-bin -bins round2,bounds,final -rounds 5 > /tmp/uniform10k.jsonl
/tmp/exact-tbm -bin-dir /tmp/seekdb-round3-bin -bins round2,bounds,final -rows 100000 -dim 256 -queries 300 -rounds 5 > /tmp/uniform100k.jsonl
/tmp/exact-tbm -bin-dir /tmp/seekdb-round3-bin -bins round2,bounds -rows 100000 -dim 256 -queries 300 -data outlier -rounds 5 > /tmp/outlier100k.jsonl
/tmp/exact-tbm -bin-dir /tmp/seekdb-round3-bin -bins round2,final -rows 100000 -dim 256 -queries 300 -data outlier -rounds 3 > /tmp/outlier-final100k.jsonl
python3 performance/exact-tbm/analyze_round3.py /tmp/uniform10k.jsonl /tmp/uniform100k.jsonl /tmp/outlier-final100k.jsonl
python3 performance/exact-tbm/run_update_comparison.py --stress /tmp/exact-stress --bin-dir /tmp/seekdb-round3-bin --out-dir /tmp/exact-update-results --data-root /tmp/exact-update-data
python3 performance/exact-tbm/compare_stress_phase.py --phase bulk_update /tmp/exact-update-results/update-*.jsonl
/tmp/exact-stress -binary /tmp/seekdb-round3-bin/round2 -data-dir /tmp/exact-recovery-fixture -rows 550000 -dim 1024 -queries 12 > /tmp/recovery-fixture.jsonl
/tmp/exact-stress -data-dir /tmp/exact-recovery-fixture -rows 550000 -dim 1024 -recovery-bins /tmp/seekdb-round3-bin/round2,/tmp/seekdb-round3-bin/recovery -recovery-rounds 3 > /tmp/recovery-comparison.jsonl
python3 performance/exact-tbm/compare_stress_phase.py --phase recovery_compare,recovery_compare_queries,recovery_compare_shutdown /tmp/recovery-comparison.jsonl
/tmp/exact-stress -binary /tmp/seekdb-round3-bin/final -data-dir /tmp/exact-final-data -rows 550000 -dim 1024 -queries 12 -bulk-update > /tmp/stress-final.jsonl
python3 performance/exact-tbm/analyze_stress.py /tmp/stress-final.jsonl
python3 performance/exact-tbm/exact_edges.py --bin-dir /tmp/seekdb-round3-bin --binary final --assert-correct
```

复现恢复对照用的是完成基础流程、尚未做全表分类 UPDATE 的 fixture；不能换成已全表更新的数据目录后仍使用基础流程真值。未覆盖 AVX512/VNNI 实机。原始文件保留在 [results/round3](results/round3)，中断的诊断及旧驱动记录不计入最终性能汇总。
