# 服务器续跑记录

- 使用现有 CODEX_LAUNCH.json 核对 `--yolo -m gpt-6-astra -c model_reasoning_effort="max"`；源码操作账户 platemo。
- `remote.py` 对计分性质的 bench 强制检查迁移完成标记及 root，并使用独占文件锁防止本实验并发。原始 lab.py 与历史成绩不改写。
- 官方规则全文已取回：results/remote-rules-markdown-response.txt，来源及 SHA256 见 remote-rules-source.json。正式规则为三轮，本地按用户要求五轮 AB/BA；E 仅作相对诊断，不是官方分数。
- 交接 residual bounds 撤下：未把已有随机查询通过等同于严格证明。remote-handoff 保存原实现；remote-nres 仅移除该实验，恢复原 maxScale 边界。原始 Intel 结果照存。
- AVX2 四行候选：沿用四行布局，用 VPMOVZXBW/VPMADDWD 精确整数点积替代 VNNI；四行共享 query loads，横向归约后以 VCVTDQ2PD 转成四个精确 float64，转置 qf 并保持原运算结合顺序。不使用饱和点积或 FMA。生产传入维度不超过 maxInt8Dim；尾维/不足四行继续原内核。
- 新独立 oracle 不调用生产 dot/fold/quantization 帮助函数；int64 点积、显式 float64 舍入检查；覆盖顺序/乱序/重复候选、非零起点、尾组、阈值相邻浮点数、NaN/Inf 及输出前后哨兵。最大整数点积检查接近支持上限。
- 首次 MVDB_NO_SIMD 全套 engine 测试失败在两处已有“必须进入 SIMD 分支”的断言；保留 remote-quad-fallback-tests 原始失败记录。修正为根据 useSIMD 检查分支是否执行，同时保留结果比较，不跳过端到端测试。
- 本机 Linux 6.5 的 memory.peak 写 0 返回 EINVAL（remote-cgroup-probe.json）。小型 T/B/M 每阶段独立新 cgroup，因此不受影响。容量脚本读取原 stress 驱动的 whole_workflow 峰值；remote_stress.py 严格检查累计峰值单调、OOM、事务与独立真值，不声称阶段峰值，原 round7 严格阶段峰值验证器保持不变。
- 首次服务器计量中止：stat(data).st_dev 为 NVMe 分区 259:4，而 cgroup io.stat 只给父磁盘 259:0。经 sysfs partition/../dev 验证后，服务器 fork 驱动按父设备计量；不将缺失 I/O 当作零，也不把所有设备相加。remote-residual-default 为失败记录，remote-residual-default-v2 是另一个独立新标签。remote_tbm.go 同时记录 filesystem_device 与完整 io_stat_raw。
- >2GiB 冷/重放对照使用服务器独立 fork：每次 trial 创建新的 cgroup，保留同一进程的 startup/cold/replay/shutdown 整体峰值；trial 之间仍仅驱逐私有日志，并以 mincore 验证。每阶段 M 为该 trial 累计值，只报告 service_total 的可比较 M/E。原历史 driver/analyzer 不改写。
- 第二个开发候选 remote-flat 将查询结果的每行 Value 分配合并成一个连续分配。第一种子查询中位数改善，但独立种子长序列反而 0.876900→0.987367s（+12.60%）、E 0.502867→0.486980。已恢复逐行分配，保留两个五轮对照及候选源码。
- 单 worker INSERT 的诊断写入较默认双 worker 慢（0.733471 vs 0.719291s）；查询没有代码变化却出现明显波动，不能把这组查询波动解释成 INSERT 并行策略的普遍查询收益。默认配置不变。
- AVX2 无绑核默认对照中位数之比改善 6.26%，但逐轮配对中位数反而 +0.26%；100k 对照中位数之比改善 3.60%、配对中位数改善 6.87%。为检验波动，另行增加固定 CPU 16-17 的同一二进制开关对照，quota/memory/swap/持久性不变；不覆盖早期结果。

## 最终矩阵与 SIMD 回退审计

- 五轮默认原生矩阵主要场景完成。127 维出现查询回退，保留原始结果并安排独立种子长查询复验；此维度不启用四行 AVX2。
- `remote-final-fallback` 驱动完整退出 0，20 条记录均 `reference_bad=0`，但旧汇总器要求跨实现响应字节哈希相同而拒绝汇总。没有修改旧汇总规则或原始数据。
- 增加独立诊断驱动 `remote_tbm_audit.go`，保留原 oracle 与阈值，额外记录有序 ID 哈希、浮点误差和样本。五轮 SIMD/纯标量 10,000 次查询有序 ID 全一致，最大 oracle 绝对误差 3.552713678800501e-15、相对误差 8.397658626025806e-16、6 ULP；各版本内部五轮响应哈希一致。这是距离加法顺序产生的末位差异，没有把它伪装成字节一致或计分结果。

## 大数据闭环与 Docker

- `remote-final-stress-v2` 完整成功：550,000×1024，2,252,800,000 原始向量字节，73 个 TopK 核对、1,099,994 行 census、6 行事务点查、4 次 SIGKILL、8,285 字节未提交尾部正确截断；全程 OOM=0。memory.max=2 GiB、swap=0，memory.peak=2,147,487,744，超配置一个 4 KiB 页，未篡改为上限值。首个 stress 标签在启动前因包装器 Path JSON 序列化失败，保留残留原文件并以 v2 重跑。
- `remote-final-large` 五轮 AB/BA 全部完成：10 个新 cgroup，源/私有副本前后 SHA256 不变，私有 inode 冷页核验通过，120 次独立响应核对。基线→最终 service T=15.368822185→15.445724623 秒，B=17,543,049,216→17,542,946,816 字节，M 两者均 2,147,487,744；E=0.500000→0.49925196。这组没有加速，保留负结果。
- Linux 6.5 大数据比较驱动在测量前改为只读 memory.peak 并每试次新 cgroup，避免旧驱动的 reset 写入；数据库 CPU 16–17、驱动 CPU18，ready 后核验数据库 affinity。
- 原始 Dockerfile 源码构建两个镜像均成功；提取镜像内二进制，SHA256 与对应原生二进制逐字节一致。实际 Docker protocol 和容器五轮 T/B/M 单独验证。

## 最终验收

- Docker public protocol 20/20 PASS；五轮容器 T/B/M 完成，写入 0.787969538→0.751325781 秒，查询 0.162709891→0.156044270 秒，B 不变，查询 M 略降；E 0.494120706→0.500660088。两镜像 ID 在计量前后未变。
- 三个种子各 40 轮随机 crash/restart 全部通过，分别核对 2,856、2,906、2,791 个存活已提交记录。
- 汇总审计 20 组完整五轮比较共 400 条阶段记录、696,000 次独立查询响应检查；2 CPU/2 GiB/swap 0/fdatasync、精确真值、I/O 合计与设备身份、OOM 为零、最终绑核均通过检查。
- 当前 95 个 MiniVectorDB 文件与 `remote-final` 快照完全一致；final 二进制与 quad 字节一致。新增脚本语法检查通过，Go 源码和驱动 gofmt -l 无输出。既有 Go 全包测试、目标 race、强制标量全 engine、golangci-lint 2.15.0（0 issues）结果均适用于这一版本。
- `agent/conversation.json` 前后 SHA256 一致，未伪造。最终报告和机器结果仅汇总本次真实记录，不改写历史成绩。
