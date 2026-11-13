本次优化基于 `d7ed759aea89e32207822ef55d65d0231ebc32cc`，分支为 `perf/exact-tbm-5500`。目标是提高 exact 查询的 T/B/M 综合表现。以下全部是本机实测，不是 OceanBase 正式分数，也不证明已经达到5500。3800/2700与具体提交的官方对应关系尚未从提交记录核实，因此表格使用提交号标识版本。

主要改动：

- 将 AVX 循环中的 legacy SSE 指令替换为 VEX 指令，消除本机 Intel CPU 上严重的切换代价；连续扫描循环展开并合并部分内存操作。256维、10万行内核微基准中，在最初六条指令修复基础上再降低约12%的耗时。
- AVX2 的覆盖等值过滤直接融合扫描候选槽位，复用主索引的量化码和参数，避免按分类复制整个量化索引。仍用原有保守阈值筛选，并对保留行计算精确 L2；没有用 ANN 替代 exact。事务覆盖、额外谓词和不满足条件的路径继续走原有逻辑。AVX512/VNNI 的大分类仍保留已有紧凑块路径。
- 根据扫描工作量决定并行度，减少小查询的线程协调；小分类不建立额外副本。
- 索引重建按最终行数分配；修正量化参数成本，计入主索引成本；分类缓存共享预算，数据变化时立即释放失效缓存。预算是数组容量与 map 成本估计，不是整个进程内存的严格上限。
- JSON 字符串使用批量扫描和 ASCII 校验，保留 UTF-8、转义及非法输入的回退语义；INSERT 常量直接求值，批次编码预留空间，减少闭包和缓冲区扩容。

默认规模：1万行、128维、1000次查询、K=10、一半等值过滤。下表为5轮交错运行的指标中位数，单位分别是秒和 MiB；B 为每个阶段整个生命周期的数据设备物理读写量，两阶段在本次负载中相同。

| 版本 | 写入 T | 查询 T | 写入 M | 查询 M | B/阶段 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 原 d7ed759（kernel2） | 0.217715 | 1.085472 | 9.824 | 14.867 | 5.582 |
| b44b5b4（v4） | 0.220999 | 0.176157 | 9.824 | 10.570 | 5.582 |
| 本分支（candidate-fix） | 0.194539 | 0.106642 | 9.812 | 12.387 | 5.582 |

相对 d7ed759，默认规模查询约快10.18倍，写入耗时降低10.6%，查询内存峰值降低16.7%。相对 b44b5b4，查询耗时降低39.5%，但查询峰值内存仍高约1.82MiB。

扩展规模：10万行、256维、200次查询；3轮交错运行。

| 版本 | 写入 T | 查询 T | 写入 M | 查询 M | B/阶段 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 原 d7ed759 | 3.328080 | 1.840318 | 112.809 | 198.867 | 104.629 |
| b44b5b4 | 3.241245 | 0.715022 | 112.625 | 116.949 | 104.629 |
| 本分支 | 2.738344 | 0.196495 | 111.172 | 149.496 | 104.629 |

扩展规模相对 d7ed759，查询约快9.37倍，写入耗时降低17.7%，查询峰值内存降低24.8%。物理 I/O 没有减少，持久化提交仍执行同步落盘。

评分口径采用[完整赛题规则](https://oceanbase.yuque.com/org-wiki-obtech-vh7w9r/hc3pio/lg8enxlmwg69unt6)：`R=(base+eps)/(candidate+eps)`，`f=R/(1+R)`，阶段内权重 `T:B:M=0.6:0.2:0.2`，阶段间 `E=(Ewrite+2*Equery)/3`，eps 为1毫秒及1MiB。对每个参考版本固定取前3轮各指标中位数，再逐轮计算候选 E，最后取 E 中位数；不把候选指标的各自中位数代入公式。

| 本地参考指标来源 | 默认规模：原 d7 / v4 / 本分支 E | 扩展规模：原 d7 / v4 / 本分支 E |
| --- | --- | --- |
| d7ed759 前3轮 | 0.501433 / 0.651968 / 0.677207 | 0.499088 / 0.606117 / 0.679689 |
| b44b5b4 前3轮 | 0.346799 / 0.494903 / 0.551450 | 0.392741 / 0.499721 / 0.613145 |

这些 E 只比较本地版本。平台固定基线、CPU、缓存预热、最终工作负载及分数转换尚未获取，不能把上述 E 换算成5500分。

验证结果：

- `go test ./... -count 1` 全部通过。
- 对新增候选扫描、缓存预算、JSON解析以及串行/并行扫描运行针对性的 `-race` 检查，通过。
- 20个公开协议用例全部通过（native `--command` 模式）。
- 30轮随机 SIGKILL、含恢复过程中再次 SIGKILL 的恢复检查通过；测试工具也补上最后一次 kill 后的验证，并显式关闭管道。
- 最终对照中所有查询均经过独立的 float64 全量距离和 PK 排序校验，三版本结果哈希一致。本分支共校验5600条查询；另校验127维尾数、异常大值和全相同向量/逆序PK，各100条。没有将采样或95%召回作为 exact 正确性标准。
- 新候选汇编对照标量实现，覆盖乱序槽位、空集合、标量尾数、1至1024维、阈值边界及 NaN/Inf 中间值；端到端覆盖删除、修改分类、事务覆盖和回滚。

环境：Intel Core Ultra 5 125H，ArchBench WSL2，Go 1.22.12 linux/amd64，ext4 `/dev/sdd`。每个写入/查询阶段使用独立进程、数据目录和 cgroup v2，限制2 CPU、2GiB、128 PID，`memory.swap.max=0`，`GOMAXPROCS=2`。驱动与参考计算不在被测 cgroup 中。查询阶段先装载并提交数据，再运行一次预热查询构建隐式索引；装载和预热不计入查询 T，但计入 B/M。B 读取数据设备的 `io.stat`，M 读取 `memory.peak`，进程退出并完成同步后才销毁 cgroup。SQL/JSON请求预先生成，响应的 JSON 解码与正确性校验放在计时外。

本机没有 AVX512，相关路径完成了汇编编译但没有实际执行验证。未运行实际 Docker 镜像，仓库 Docker 的 Go 1.22.2 与本地版本不同；未进行平台提交。本地结果也不覆盖隐藏用例或数据规模超过内存时的完整压力验证。

复现时准备 Linux 下的 Go 1.22 与 cgroup v2 root 权限，数据目录应放在 ext4 上。三个二进制分别由原 d7、b44 和当前分支构建，名字与下列命令对应。基准会创建临时数据库，正常结束或失败退出时清理自己创建的目录和 cgroup。

```sh
mkdir -p /tmp/seekdb-review-bin
git worktree add --detach /tmp/seekdb-tbm-d7 d7ed759aea89e32207822ef55d65d0231ebc32cc
git worktree add --detach /tmp/seekdb-tbm-v4 b44b5b4e764912a356ecaeb13452392166806585
go build -C /tmp/seekdb-tbm-d7/minivectordb -o /tmp/seekdb-review-bin/kernel2 ./cmd/minivectordb
go build -C /tmp/seekdb-tbm-v4/minivectordb -o /tmp/seekdb-review-bin/v4 ./cmd/minivectordb
go build -C minivectordb -o /tmp/seekdb-review-bin/candidate-fix ./cmd/minivectordb
go build -o /tmp/tbm-bench performance/exact-tbm/tbm_bench.go
sudo /tmp/tbm-bench -bins kernel2,v4,candidate-fix -rounds 5 > /tmp/tbm-10k.jsonl
sudo /tmp/tbm-bench -rows 100000 -dim 256 -queries 200 -bins kernel2,v4,candidate-fix -rounds 3 > /tmp/tbm-100k.jsonl
python3 performance/exact-tbm/analyze_tbm.py /tmp/tbm-10k.jsonl /tmp/tbm-100k.jsonl
```

`-bin-dir` 和 `-data-dir` 可更改二进制目录与临时数据根目录。`-bins` 支持 `candidate-fix:MVDB_NO_FUSED_CANDIDATES=1`，用于对照新候选内核；原有 `MVDB_NO_CATBLOCK`、`MVDB_NO_PREFILTER`、`MVDB_SCAN_WORKERS` 也可组合使用。原始指标、分析结果与验证输出保存在 [results](results)。
