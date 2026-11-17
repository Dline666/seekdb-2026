# T/B/M 深入优化研究记录（2026-11-17）

本轮固定比较基线为上一轮 `remote-final`，SHA256 `d1bb32fdaf2e12ba0b5d02bd37d8c2ff072e611fa419994c4641ae652bf55541`。旧报告、失败记录和用户对话均保护；本目录结果是新的实验，不能覆盖或追认旧成绩。计分实验与剖析串行，CPU profile、strace、perf 数据不作为性能成绩。

## 已观察到的瓶颈

- 默认 10,000 行、128 维：101 次 fdatasync 的内部墙钟合计约 0.649 秒；实际写入 5,441,883 字节。写入总时间约 0.72 秒，同步延迟是第一优先级。strace `-c` 的系统调用 CPU 时间不等于等待持久化的墙钟时间，不能用其 4.6 ms 反驳内部测量。
- 默认查询 profile 使用 2,000 条独立真值查询重复 20 次以取得足够样本，未计分。1.82 CPU 秒中四行顺序 AVX2 内核占 29.1%，候选内核 8.2%，精确距离回验 7.1%。查询阶段共分配 347,073,960 字节、3,003,226 个对象，约 8.7 KiB / 75 对象每请求；369 次 GC，GC CPU 约 0.078 秒。分配采样中 lexer token 数组约 32%，结果/表达式构建约 20%，查询向量 float64 展宽约 12%。
- 100,000 行、256 维：顺序与候选量化扫描占 CPU 样本的约 78.4%，精确回验 9.2%；GC CPU 仅约 0.0062 秒，相对 5.64 CPU 秒很小。1,001 次同步合计约 6.23 秒。放宽 GOGC 的潜在收益主要在小查询，而不是大扫描。
- 当前 AMD 分支默认使用 fused candidate 内核，已经不构建 category code copies。单纯关闭 `MVDB_NO_CATBLOCK` 对这里不是有效的布局消融。行位置 map 的键、值均无指针；不能把替换 Go map 直接宣传为消除全部 GC 扫描。

证据：`profiles/default/`、`profiles/100k/`、`profiles/strace-v2/`；`results/profile-*-{query,alloc}-top.txt`。首次 strace 包装的 affinity 校验失败已保留为 `diagnostic-strace`，修正后重新独立执行为 `diagnostic-strace-v2`。

## 资料及适用范围

访问日期均为 2026-11-17；外部资料只提供机制假设，本机结果另列。

| 来源 | 适用版本及借鉴机制 | 本机验证安排 |
|---|---|---|
| [Go GC guide](https://go.dev/doc/gc-guide) | GC 模型自 Go 1.19；GOGC 在 CPU 与堆之间取舍，GOMEMLIMIT 不覆盖外部 mmap 页缓存 | Go 1.22.2 的 runtime/metrics、alloc/heap profile；独立 GOGC 变量 |
| [Go 1.22.2 runtime/map.go](https://raw.githubusercontent.com/golang/go/go1.22.2/src/runtime/map.go) | 无指针 key/value 的 bucket 可免扫描；overflow 元数据仍需维护 | 核对 int64→int64、int64→int32 两类映射；按实际 heap 和 cgroup M 决策 |
| [Go PGO](https://go.dev/doc/pgo)、[Go 1.22 发布说明](https://go.dev/doc/go1.22) | 1.22 支持 PGO 内联/去虚拟化；代表性 profile 与独立数据验证必需 | 如实验，使用同一 1.22.2 和单独二进制，显式 `-pgo=off` 对照 |
| [Linux 6.5 cgroup v2](https://www.kernel.org/doc/html/v6.5/admin-guide/cgroup-v2.html) | 页缓存计入 memory，writeback 归属、I/O 层次需校准 | 新 cgroup、memory.peak、memory.stat、259:0 物理设备；swap=0 |
| [fallocate(2)](https://man7.org/linux/man-pages/man2/fallocate.2.html) | KEEP_SIZE 预分配不改变 EOF；并不保证避免后续 extent/EOF 同步成本 | 4 MiB 预留候选五轮：写入中位数 -1.28%，查询 +8.15%，综合 E 下降，暂不保留 |
| [madvise(2)](https://man7.org/linux/man-pages/man2/madvise.2.html) | 顺序/随机提示影响预读和回收；RSS 减少不保证 cgroup 页缓存减少 | 超内存场景另剖析；只操作本实验私有文件，不执行全机 drop_caches |

## 实验优先级

1. 同步占主导：空间预留、已初始化分块写入的有限候选；必须保留每次成功确认前 fdatasync，计入额外物理写入和缓存。
2. 小查询分配：有界 token / 查询 scratch 复用，验证源字符串与返回值生命周期；运行时配置单独消融。
3. 候选结构：缓存安全的已有分块上界，减少每查询重复合并；构建、失效和预算全部计入。此前 residual bound 继续撤下。
4. 大扫描：用 cache/fault/CPU 证据判断算力与数据搬运；只有综合收益成立才扩大索引或引入复制布局。

所有 E 只代表本机相对基线；未知官方固定基线，不推测正式分数或名次。

## 时间口径与边际收益

保留旧口径 `local_E` 供与上一轮比较；本轮主要使用 `prepared_local_E`：写入 T 包括启动、DDL、装载、退出，查询 T 包括启动、预热/索引构建、查询和退出。查询进程的重复 fixture 装载在写入阶段已有对应成本；另提供 `whole_process_local_E`，将该重复装载也算入查询 T，防止口径选择隐藏代价。三种 E 均采用用户给定的 epsilon、60/20/20 和 write/query=1:2 权重；B/M 始终使用完整进程加最终写回。

第一轮 GOGC=100 已证明：少约 2.8% 查询时间不足以补偿 23.7% 的查询 cgroup M 增长，单纯减 GC 不保留。数据结构方向先缓存少量已有上界，而不是复制全部类别代码。超内存方向检查 256 MiB 派生索引预算是否造成大量未量化尾部：在 M 已接近 2 GiB 上限时，增加受控的有效代码覆盖可能减少原始向量反复读盘，必须把完整构建成本计入服务期比较。

## 系统测量限制

服务器没有 perf 命令，但 sudo 的 `perf_event_open` 可用。本目录 `perfwrap.c` 只绑定被测子进程并继承其线程，不进行整机采样或修改内核设置。硬件事件发生 multiplex：原始值同时附带 enabled/running 时间，需要按比例解释；通用 cache 事件不能未经 AMD PMU 映射确认就称为 LLC miss。第一次版本把 software context-switch 事件也限定为用户态，得到的零值无诊断意义，不能解释为没有上下文切换。

`fdatasync` 仍须同步恢复文件内容必需的元数据，文件长度是其中一项；初始化块方案由此提出，但是否减少等待以本机实测为准。[Linux fsync/fdatasync 文档](https://man7.org/linux/man-pages/man2/fsync.2.html)；进程继承计数和 multiplex 解释参见 [perf_event_open 文档](https://man7.org/linux/man-pages/man2/perf_event_open.2.html)。

## 超内存场景的机制证据

相同私有日志、单独诊断运行中，旧版查询段的 user/system CPU 为 1.027 / 7.129 秒，单遍候选构建版为 0.973 / 3.655 秒；major faults 从 89,350 降到 46,366，minor faults 从 101,120 降到 54,131。查询段分配反而从 6.394 MB 增到 13.112 MB，两个版本该段均没有 GC。这里的收益主要来自少一次跨越页缓存容量的日志扫描和相应缺页、回收与内核工作，不是更少 Go 分配或更快 AVX2 算术。CPU profile 的采样工具不能完整归因内核栈，因此结合 rusage 和物理 I/O，而不把用户态平坦 profile 当成全部时间。

两遍类别构建先计数，再扫描每行的 VARCHAR 填充数组；由于 VARCHAR 与 1024 维向量共处日志行，读取很少的字符串仍需触碰整片数据的页。单遍按组增长少量 int32 行号数组，保留原有有序访问和精确过滤，只付出可计量的容量冗余。它不复制向量，不替换持久化格式，不增加索引预算。保留 256 MiB 预算；768 MiB 虽然暖重放更快，但构建和回收成本使完整服务退步，另见负例。

进一步改为列式持久化/附加列文件会引入写放大、恢复协议与同步责任，本轮没有证据支持比单遍扫描更好的投入收益，不把这种未实现架构列为已经排除的实测结果。原有行号映射采用无指针整数键值，替换为稠密数组还受稀疏 BIGINT、删除和事务覆盖约束；本轮未宣称更换 map 的收益。

## Docker 为什么削弱原生收益

独立诊断使用同一基线、同一 2,000 条查询重复 10 次。原生查询墙钟为 1.0166 秒，数据库 cgroup CPU 为 0.8758 秒；实际 Docker 链路为 3.2080 秒和 1.1358 秒，没有 CPU throttling。启动分别为 0.0027 / 1.0482 秒，退出为 0.0015 / 0.5620 秒。差值大部分不在数据库计算内，包含 Docker 启动/退出、管道转发与等待；不能把这一个诊断样本进一步精确分摊给某个 daemon 函数。数据库计算加速会被端到端固定成本稀释，这也使减少 fdatasync 的局部写入优势不足以补偿双倍物理写入。

证据在 `results/transport-native.jsonl`、`results/transport-docker.jsonl`（诊断用途，非计分；实际标签以命令索引为准）以及最终五轮 Docker 对照。

## 综合 E 的边际取舍

对某指标的归一化项，关于该指标 log 成本的导数绝对值是 `w*f(R)*(1-f(R))`。在 R=1 时最大，在趋于饱和时降低。加上阶段权重，查询 T、写入 T 的最大边际系数分别为 0.1、0.05；不能把同样百分比的写入微优化当作相同综合收益。B 在两个阶段都翻倍，epsilon 可忽略时会令总 E 约损失 0.0333。实际 Docker 写入端到端 T 的降幅不足以抵消该损失，故最终撤下初始化日志块。

最终主口径是同轮配对基线 E=0.5；原先前三轮中位数锚点与原始阶段 E 原样保留。配对变化的中位数不等于两组中位数之比，特别是短查询和 4 MiB 部分索引场景；报告同时给出分布、逐轮值及两种时间口径。所有版本的 B/M 均含页缓存和准备，减少 TotalAlloc 不自动视为 M 下降。

## 新编码与缓存的正确性论据

AVX2 编码仍使用与 Go 1.22.2 原路径相同的 float32 min/scale，再用 float64 减法和除法。商非负，以截断整数加“分数部分 ≥ 0.5”实现原 math.Round 的正数半值规则，不采用默认 nearest-even 整数转换。maxInt8Dim=33025 时最大平方和为 2,147,450,625，小于 int32 上限；整数累加转 float64 精确，原 qparam 表达式顺序保留。常数、scale 下溢、无限 scale、奇数尾部及最大长度有标量与独立 big.Float/有理数 oracle；guard page 验证读写边界。CPU 不支持或显式关闭时使用原 Go 路径。

candidate bound cache 只合并既有保守 bound 的最大 scale/absolute coordinate，分块单位是候选数组中的 128 个位置。对任意子集，两个最大值不会超过包含它的块；工作线程和批次不得跨缓存块边界。原误差上界与阈值公式均未改变，因此没有恢复被撤下的 residual bound。缓存与候选数组一起按 generation 失效，所有保留容量在共享预算内预先计费；事务覆盖等不满足 fused 路径条件的查询继续走原精确路径。

## 最终 Go 验证与反向信号

同一默认诊断序列（40,000 查询）的 TotalAlloc 347,073,960→239,568,192 字节（约 -31.0%），分配对象 3,003,226→2,762,885，GC 369→245 次，GC CPU 0.07791→0.05240 s；最终存活堆和 cgroup M 没有同幅下降。原 lexer token 数组分配从主要采样项消失，仍有表达式/结果、向量解析和距离/输出成本。该诊断序列不用于计分。

100k 诊断也保留反向信号：query TotalAlloc 117.386→91.794 MB，但 user+system CPU 5.660→6.177 s；只有一次带采样构建，不能据此作代码因果归因或说所有扫描加速。最终又增加独立种子 41151 的五轮 100k×256、5,000 查询完整对照（`final-100k-long`），专门检查准备收益被摊薄后的取舍。索引准备 CPU 样本中 quantizeRow（含子调用）约 70 ms，其中新编码函数 20 ms；这种短 profile 粒度只有 10 ms，具体加速率以非采样对照为准。

Go 1.22.2 编译器 `-m=2` 确认量化函数的 v/codes 不逃逸；eqset map 临时查询的 `string(key)` 不逃逸，而插入键正常分配拥有的数据。SSA BCE 输出仍含必要的 slice 边界检查，没有把“使用汇编”写成全路径零检查。编译器输出完整留在 `results/final-escape-{parser,engine}.txt`。未使用新版本 runtime、PGO、全局 unsafe 视图或额外 goroutine 隐藏成本。默认双 CPU 分区和并行阈值保持原实现。

大型诊断的硬件计数按 enabled/running 归一化后，用户态 instructions 约 24.35→24.29 billion，通用 cache-miss 约 97.9→97.6 million，变化很小；不能用它们宣传显著的用户态指令或 LLC miss 改善。整个进程软件 page-faults 370,557→287,236、context-switches 108,827→77,025，结合分段 rusage 才支持缺页/内核工作下降的解释。原始 multiplex 时间与缩放值分别见 `profiles/large/*/perf.json`、`results/perf-large-scaled.json`。

补充的非采样长序列 `final-100k-long` 没有重现上述诊断 CPU 退步：纯查询中位 1.74997→1.68962 s，准备计入查询 T 2.00707→1.88698 s。该证据足以保留当前组合，但一次诊断与非采样构建的差别仍作为限制，不将差异强行归因于某条指令。
