# MiniVectorDB 远端极限优化实验结果

日期：2026-11-17。最终版本 `remote-final`。所有数据为本机真实运行结果；未正式提测、未推送 GitHub、未发送对外消息。

## 结论与保留的实现

相对迁入 Round7 基线，默认 10,000 行/128 维/1,000 次查询的五轮中位数：写入 0.755072 → 0.709624 秒，查询 0.077108 → 0.059816 秒（-22.43%）；本地 E 为 0.499999 → 0.526694。默认第二个独立种子和 5,000 查询长负载也获益。

新四行 AVX2 的同二进制开关实验，固定数据库 CPU 16–17、驱动 CPU 18，20,000 次查询从 1.182579 → 1.031031 秒，变化 -12.82%。这组同机测量支持本机 AVX2 的收益；Intel 上的 VNNI 微基准单独保留为历史记录。

- 保留：四行 AVX2 精确整数点积、共享查询载入、四路 float64 epilogue；保留迁入的 decimal/vector parser、JSON 解码、fast INSERT、释放保留 SQL 字符串，以及 2026 年 golangci-lint 2.15.0。
- 撤下：residual bounds 的实现和开关。已有普通测试不能补足独立推导/oracle；本轮没有声称其正确，也没有把它留在最终代码中。`remote-handoff` 等快照保留原实验。
- 拒绝：连续分配投影结果的 `remote-flat`。初测有利，独立种子长负载确认查询变慢约 12.6%，最终恢复原实现。
- 不作全场景加速声明：127 维短负载回退，复验长负载基本持平；超 2 GiB 查询服务约慢 0.5%。所有不利结果列在后文。

本次新增代码相对交接版本的可读差异在 [REMOTE_IMPLEMENTATION.patch](REMOTE_IMPLEMENTATION.patch)，最终源码快照在远端 `experiment-runtime/remote-final`。

## 环境与统计口径

- AMD Threadripper PRO 5975WX，AVX2/FMA 可用，AVX-VNNI/AVX512 不可用；Linux 6.5.0-28-generic。Go 1.22.2、GOAMD64=v1、CGO_ENABLED=0，运行时 GOMAXPROCS=2、GOGC=20、GOMEMLIMIT=1288490188。
- 固定 cgroup v2：2 CPU（200000/100000）、2 GiB memory.max、swap 0、fdatasync；源码编辑及 Codex 为 platemo，cgroup 驱动 sudo -n。启动记录确认 Codex --yolo / gpt-6-astra / max。
- MIGRATION_COMPLETE.json 出现且历史传输结束后才开始计分性质测量。本轮实验串行；没有整机 drop_caches，没有修改其他用户任务。
- 最终小型矩阵和同二进制隔离比较固定数据库 CPU 16–17、驱动 CPU 18。早期未绑核及仅绑定数据库的结果保留，不能与后期结果混为同一调度条件。capacity 单版本验证不绑核，配额仍为 2 CPU。
- 每个版本/阶段新数据库和新 cgroup；五轮 AB/BA。每组固定相同种子和请求序列，多组使用独立种子。TopK 主键/顺序逐项精确核对；距离由外部独立标量 oracle 按协议浮点容差核对；真值生成与验证在计量之外。
- T：写入批次持久化端到端，或预热后的完整查询序列；B/M：从启动、装载、准备、预热到退出与写回的完整数据库进程/cgroup，M 包含文件页缓存。原始记录另留 startup/warmup/request_total。
- 数据文件系统是 259:4 分区，io.stat 在本机归属父 NVMe 259:0；通过 sysfs 解析并记录两者，不忽略缺失计数、不把未知 I/O 当零。
- Linux 6.5 memory.peak 写入 0 返回 EINVAL。小型矩阵用新 cgroup 天然得到整次峰值；capacity 报整段流程峰值；大数据比较每试次新 cgroup，只对 service_total 计算 M/E。
- E 使用 T/B/M=0.6/0.2/0.2、写/查=1:2、平滑量 1ms/1MiB/1MiB，固定基线前三轮各指标中位数，再取五个完整轮次 E 的中位数。表格时间是各指标中位数，不拼接出虚构的“最好一轮”。

已完整阅读 [当前比赛规则](https://oceanbase.yuque.com/org-wiki-obtech-vh7w9r/hc3pio/lg8enxlmwg69unt6)、STDIO_PROTOCOL.md 和公开检查包。官方文档为三轮，本地按用户要求五轮；本地 E 仅用于对照，不映射正式性能分数或排名。规则原文与 SHA256 见 `results/remote-rules-*`。

## 最终原生矩阵：基线 → 最终版本

时间单位秒；查询变化是“中位数之比−1”，负值为加速。配对列是五个同轮比值的中位数，明确区分两种统计。所有行五轮完整、独立真值零错误、两版本响应哈希相同。

| 场景（行/维/查询；种子） | 写入 T | 查询 T | 查询变化 | 配对查询比 | 本地 E |
|---|---:|---:|---:|---:|---:|
| [default (10000/128/1000; 20261118)](results/remote-final-default-summary.json) | 0.755072 → 0.709624 | 0.077108 → 0.059816 | -22.43% | 0.7638 | 0.499999 → 0.526694 |
| [seed9001 (10000/128/1000; 9001)](results/remote-final-seed9001-summary.json) | 0.751556 → 0.710136 | 0.074551 → 0.065109 | -12.66% | 0.9298 | 0.500022 → 0.516922 |
| [long (10000/128/5000; 2028)](results/remote-final-long-summary.json) | 0.759924 → 0.729052 | 0.305488 → 0.276120 | -9.61% | 0.8998 | 0.500824 → 0.511732 |
| [100k (100000/256/500; 991)](results/remote-final-100k-summary.json) | 8.464989 → 7.742439 | 0.261312 → 0.231557 | -11.39% | 0.9009 | 0.500450 → 0.517206 |
| [tail127 (10000/127/1000; 1271)](results/remote-final-tail127-summary.json) | 0.757943 → 0.711763 | 0.188553 → 0.228989 | +21.45% | 1.0965 | 0.512281 → 0.496419 |
| [tail127-confirm (10000/127/5000; 1279)](results/remote-final-tail127-confirm-summary.json) | 0.758451 → 0.718533 | 0.805042 → 0.805900 | +0.11% | 1.0046 | 0.498968 → 0.502619 |
| [dim1024 (10000/1024/500; 10241)](results/remote-final-dim1024-summary.json) | 1.529829 → 1.137136 | 0.103379 → 0.096715 | -6.45% | 0.9355 | 0.500922 → 0.521528 |
| [constant (10000/128/500; 951)](results/remote-final-constant-summary.json) | 0.740120 → 0.701366 | 0.148384 → 0.136191 | -8.22% | 1.0691 | 0.500200 → 0.509197 |
| [outlier (10000/128/500; 753)](results/remote-final-outlier-summary.json) | 0.764145 → 0.713732 | 0.041131 → 0.036632 | -10.94% | 0.8981 | 0.506050 → 0.519143 |
| [multiline (10000/128/1000; 357)](results/remote-final-multiline-summary.json) | 0.959398 → 0.743105 | 0.084486 → 0.065630 | -22.32% | 0.8213 | 0.497372 → 0.533627 |
| [partial4m (10000/1024/100; 517)](results/remote-final-partial4m-summary.json) | 1.494502 → 1.113723 | 0.052857 → 0.052631 | -0.43% | 1.0016 | 0.495877 → 0.510778 |

资源单位 MiB。B 在这些新库场景中两阶段相同；逐轮 rbytes/wbytes 与完整 io.stat 仍在原始 JSONL。M 分别给写入进程及查询进程全生命周期峰值。

| 场景 | B（写/查相同） | 写入 M | 查询 M |
|---|---:|---:|---:|
| default | 5.582 → 5.582 | 9.371 → 9.609 | 11.551 → 11.793 |
| seed9001 | 5.582 → 5.582 | 9.359 → 9.477 | 12.016 → 11.922 |
| long | 5.582 → 5.582 | 9.328 → 9.477 | 12.172 → 12.191 |
| 100k | 104.629 → 104.629 | 110.406 → 110.973 | 146.664 → 146.777 |
| tail127 | 5.543 → 5.543 | 9.398 → 9.453 | 11.703 → 12.039 |
| tail127-confirm | 5.543 → 5.543 | 9.418 → 9.480 | 11.969 → 12.129 |
| dim1024 | 39.762 → 39.762 | 50.930 → 50.969 | 61.039 → 61.453 |
| constant | 5.582 → 5.582 | 9.379 → 9.469 | 11.898 → 12.062 |
| outlier | 5.582 → 5.582 | 9.320 → 9.648 | 11.613 → 11.680 |
| multiline | 6.066 → 6.066 | 10.094 → 10.312 | 12.051 → 12.664 |
| partial4m | 39.762 → 39.762 | 50.734 → 50.910 | 52.027 → 54.660 |

这部分累计独立核对 161,000 次查询响应（包含基线和最终版本）。127 维首次短负载查询 +21.45%；第二种子 5,000 次查询 +0.11%，不据此删除首次负结果。4 MiB 部分索引场景的查询内存增加约 2.63 MiB，也保留在选择依据中。常数和部分索引场景的配对查询比中位数分别为 1.0691、1.0016；它们的中位数之比虽有利，不能单独认定为稳定查询收益。

## 优化试验与不利结果

| 试验 | 查询 T（前→后，秒） | 查询变化 | 本地 E |
|---|---:|---:|---:|
| [remote-residual-default-v2](results/remote-residual-default-v2-summary.json) | 0.101141 → 0.103907 | +2.74% | 0.501769 → 0.498229 |
| [remote-avx2-default](results/remote-avx2-default-summary.json) | 0.521137 → 0.488510 | -6.26% | 0.499421 → 0.507781 |
| [remote-avx2-100k](results/remote-avx2-100k-summary.json) | 0.259454 → 0.250113 | -3.60% | 0.505387 → 0.509364 |
| [remote-projection-default](results/remote-projection-default-summary.json) | 0.495933 → 0.454411 | -8.37% | 0.501363 → 0.510623 |
| [remote-projection-confirm](results/remote-projection-confirm-summary.json) | 0.876900 → 0.987367 | +12.60% | 0.502867 → 0.486980 |
| [remote-insert-workers](results/remote-insert-workers-summary.json) | 0.073680 → 0.090611 | +22.98% | 0.500924 → 0.479053 |
| [remote-avx2-pinned](results/remote-avx2-pinned-summary.json) | 0.631540 → 0.650160 | +2.95% | 0.512223 → 0.509109 |
| [remote-avx2-isolated](results/remote-avx2-isolated-summary.json) | 1.182579 → 1.031031 | -12.82% | 0.499716 → 0.513863 |

- `remote-residual-default-v2` 是“含 residual → 撤下 residual”，性能略退仍撤下，因为正确性证明未完成。
- `remote-avx2-default/100k` 是未绑定 CPU 的 nres→quad；默认场景虽然中位数之比有利，同轮配对比的中位数约 +0.26%，早期结果不能单独证明稳定收益。
- `remote-avx2-pinned` 只固定数据库而驱动未绑定，查询回退约 2.95%；保留该记录。随后 `remote-avx2-isolated` 将驱动固定到独立核心，五轮均获益。
- `remote-projection-default` 首测有利，但 `remote-projection-confirm` 明确回退，连续结果分配已撤销。
- `remote-insert-workers` 在当时的 flat 快照对比单/双 worker；双 worker 写入中位数略好，查询没有对应代码变化且波动大，维持原默认值，不宣称查询收益。
- AMD 微基准（`remote-avx2-micro.txt`）解释四行 AVX2 的内核收益，最终选择仍依据完整 T/B/M 与真值。

历史 Intel/WSL 五轮结果原样保留，以下只用于交接审计，不能作为本机提速证据：

| 历史比较 | 查询 T（秒） | 查询变化 |
|---|---:|---:|
| [combined-default](results/combined-default-summary.json) | 0.536462 → 0.514465 | -4.10% |
| [quad-default](results/quad-default-summary.json) | 0.308661 → 0.330675 | +7.13% |
| [quad-toggle](results/quad-toggle-summary.json) | 1.027351 → 1.005671 | -2.11% |
| [quadboth-default](results/quadboth-default-summary.json) | 0.515094 → 0.499048 | -3.12% |
| [residual-100k](results/residual-100k-summary.json) | 0.420623 → 0.399299 | -5.07% |
| [residual-default](results/residual-default-summary.json) | 0.454279 → 0.456916 | +0.58% |

## AVX2 正确性依据

新增内核使用 VPMOVZXBW 将行字节扩展为 0..255 的 int16，再用 VPMADDWD 与 0..255 的查询码计算精确 int32 点积。两个乘积之和最多 130050；调用契约 d≤floor((2³¹−1)/65025)，所以全部正项总和以及所有中间分组归约都不溢出。测试覆盖的最大合法 32 倍数维度为 33024，其上界是 2,147,385,600。int32→float64 转换精确，四行之间只做独立归约。

四路 epilogue 的乘加顺序与原折叠距离逐操作相同，显式使用乘和加而不使用 FMA；仅 ordered greater-than 丢弃候选，等值与 NaN 保留。非负阈值下省略负近似距离的 clamp 不改变筛选结果；负阈值、维度非 32 倍数、小行数及尾组均走原内核。幸存候选最终仍计算原来的精确 L2。

新测试的 oracle 独立用 int64 循环点积和逐步 float64 舍入，不调用生产 dot/fold/quantization；覆盖顺序/候选扫描、非零起点、重复/逆序候选、边界维度、尾组、阈值两侧相邻浮点数、相等、0、Inf/NaN、异常折叠常量及输出前后/未用区域哨兵。最大维度全 255 用例通过。原 folded/预筛选及端到端测试也通过。

## 正确性、持久化、恢复与资源

- 最终源码 `go test ./... -count=1` 全包通过；golangci-lint 2.15.0 fmt/lint 通过，0 issues。覆盖沿用的 decimal/vector parser 大规模随机及精确边界测试。
- 相关 engine/proto/sqlparse 的 race 测试通过；强制 MVDB_NO_SIMD=1 的完整 engine 测试通过。
- 独立 SIMD/标量回退审计：10,000 次查询、有序主键哈希一致；与独立 oracle 最大绝对误差 3.5527136788005009e-15、相对误差 8.397658626025806e-16，6 ULP。原字节哈希汇总失败保留，回退审计不计算 E。
- 公开 native protocol 20/20，实际 Docker protocol 20/20；最小 BIGINT、常数抵消、过滤、关闭预筛选等 8 个边界组合全部通过。
- 三个独立种子各 40 轮随机 kill/restart 通过，核对已确认提交、未确认操作全有或全无、未提交事务不泄漏及恢复后 TopK。
- 550,000×1024 float32 流式写入，原始向量 2,252,800,000 字节，超过 2 GiB；最终日志 4,313,968,692 字节。73 次 TopK、1,099,994 行主键/类别/范数核对、6 行事务可见性核对全部正确。
- capacity 明确实施 4 次 SIGKILL：已确认提交后、带已刷新但未提交尾部、恢复 ready 前、全表 UPDATE 提交后。未提交尾部 8,285 字节被正确截断；bulk UPDATE 影响 549,997 行，恢复后再次逐行核对。
- memory.max 始终 2,147,483,648 字节、swap 0；整段 memory.peak 为 2,147,487,744 字节（高出 4,096 字节）；oom=oom_kill=0。memory.events.max 有回收压力事件，未隐去或误称完全无内存压力。

capacity 阶段 T/B 摘要（M 为同一流程累计峰值，不能据此声称阶段独立内存）：

| 阶段 | T 秒 | B MiB | 累计 M MiB |
|---|---:|---:|---:|
| initial_startup | 0.002852 | 0.000 | 1.742188 |
| write | 94.594315 | 2198.934 | 2048.003906 |
| query_after_write | 5.668182 | 4437.309 | 2048.003906 |
| updates_deletes_rollback | 0.137124 | 0.273 | 2048.003906 |
| query_after_updates | 3.657162 | 3205.129 | 2048.003906 |
| kill_after_acknowledged_commits | 0.132338 | 0.000 | 2048.003906 |
| recovery_after_commit_kill | 0.902213 | 780.762 | 2048.003906 |
| query_after_commit_recovery | 5.727250 | 4711.348 | 2048.003906 |
| uncommitted_tail_flushed | 0.069271 | 19.770 | 2048.003906 |
| kill_with_uncommitted_transaction | 0.149066 | 0.000 | 2048.003906 |
| kill_during_recovery | 0.011304 | 1.863 | 2048.003906 |
| final_recovery | 0.964486 | 791.766 | 2048.003906 |
| query_after_final_recovery | 6.677693 | 5378.637 | 2048.003906 |
| final_full_census | 5.454018 | 1332.746 | 2048.003906 |
| bulk_update | 9.421907 | 6801.551 | 2048.003906 |
| query_after_bulk_update | 28.675359 | 25478.027 | 2048.003906 |
| kill_after_bulk_commit | 0.149387 | 0.000 | 2048.003906 |
| recovery_after_bulk_commit | 2.454282 | 2861.852 | 2048.003906 |
| query_after_bulk_recovery | 14.755502 | 12412.645 | 2048.003906 |
| final_full_census | 8.624297 | 3897.828 | 2048.003906 |
| graceful_shutdown | 0.101802 | 0.000 | 2048.003906 |

## 超 2 GiB 五轮查询服务比较

使用 capacity 真实生成的最终数据库；一个私有副本、源与副本前后 SHA256/元数据不变。每次启动和冷批次前只驱逐该私有 inode 的文件页，并用 mincore 验证；同进程 replay 不驱逐。全表最终类别为 c9，因此 c9 过滤覆盖全表，不能当作高选择性过滤收益。6 个独立查询案例，每次冷/replay 响应均核对，累计 120 次；10 个试次使用 10 个不同 cgroup。

| 范围 | 基线 T 秒 | 最终 T 秒 | 基线 B MiB | 最终 B MiB |
|---|---:|---:|---:|---:|
| startup | 2.214971 | 2.210835 | 4114.125 | 4114.125 |
| cold | 12.820577 | 12.822766 | 12586.367 | 12587.293 |
| replay | 0.315422 | 0.317842 | 29.781 | 29.820 |
| shutdown | 0.066089 | 0.066363 | 0.000 | 0.000 |
| service_total | 15.368822 | 15.445725 | 16730.355 | 16730.258 |

完整 service_total M：2048.003906 → 2048.003906 MiB；服务级 E：0.500000 → 0.499252。完整服务 T 变化 +0.50%，没有大数据加速证据。这个 E 没有写入项目，不能与默认综合 E 合并。冷/replay M 只是累计峰值，未计算它们各自的 E。

## 实际 Docker 构建及链路

使用根目录原始 Dockerfile 从两份源码快照构建，Ubuntu 24.04 基础镜像按 digest 固定。实际提取并核对后，两份镜像内二进制都与对应原生二进制逐字节相同，编译器均为 Go 1.22.2；镜像身份、完整 build info 与构建日志保留。

| 版本 | 镜像 ID | 镜像内二进制 SHA256 | 与原生相同 |
|---|---|---|---|
| remote-base | `sha256:02adc8cc2cccd84706d7e52cbe1643878623d5a17f0f77619d37cceeb3841883` | `b190079fe993ad33a0a48006d167a41b98b12bba29c29248c1ae97c5522dd99d` | True |
| remote-final | `sha256:dd7952f06a59eacaa578166e16a6883bc7d9926956447f6ee8b2ef8f5a72de2b` | `d1bb32fdaf2e12ba0b5d02bd37d8c2ff072e611fa419994c4641ae652bf55541` | True |

镜像内 Go build info：

```text
/home/platemo/seekdb-experiments/20261117/experiment-runtime/bin/remote-docker-remote-base: go1.22.2
	path	minivectordb/cmd/minivectordb
	mod	minivectordb	(devel)	
	build	-buildmode=exe
	build	-compiler=gc
	build	-trimpath=true
	build	CGO_ENABLED=0
	build	GOARCH=amd64
	build	GOOS=linux
	build	GOAMD64=v1
/home/platemo/seekdb-experiments/20261117/experiment-runtime/bin/remote-docker-remote-final: go1.22.2
	path	minivectordb/cmd/minivectordb
	mod	minivectordb	(devel)	
	build	-buildmode=exe
	build	-compiler=gc
	build	-trimpath=true
	build	CGO_ENABLED=0
	build	GOARCH=amd64
	build	GOOS=linux
	build	GOAMD64=v1
```

Docker 五轮：写入 T 0.787970 → 0.751326 秒；查询 T 0.162710 → 0.156044 秒（-4.10%）；本地 E 0.494121 → 0.500660。写入 B 5.582 → 5.582 MiB，查询 B 5.582 → 5.582 MiB；写入 M 9.910 → 10.129 MiB，查询 M 12.215 → 12.105 MiB。独立真值与跨版本响应哈希检查通过。

Docker 在持久父 slice 中运行并核验数据库 cgroup 归属，容器退出后再同步数据库文件、采集完整 B/M，避免丢失退出时的写回/峰值。公开 protocol 使用真实 Docker、network none、只读根文件系统、drop capabilities、2 CPU/2 GiB/无 swap，20 项全部通过。

## 失败记录、边界与复现入口

- `remote-residual-default`：首次设备计量找不到分区 io.stat；0 条有效测量，未计分。通过 sysfs 证实写回统计归属父 NVMe 后另起 v2。
- `remote-quad-fallback-tests`：两项旧测试在强制关闭 SIMD 后仍要求 SIMD 计数增加；修复测试的条件断言，完整标量引擎测试随后通过，生产逻辑未改。
- `remote-final-fallback`：驱动完整成功且独立真值零错误，但 SIMD/标量距离末位不同使原严格响应哈希汇总失败。保留原记录，另以诊断驱动审计，不计算该比较的 E。
- `remote-final-stress`：包装器将 pathlib.Path 写入 JSON，启动数据库前失败；失败文件未覆盖，修复 str(GO) 后用 stress-v2 完成全流程。

- 正式隐藏 MySQL case、准备/请求时限及平台排名未运行；未正式提测、推送或发送外部消息。
- AMD 5975WX 不支持 AVX-VNNI/AVX512；相关历史代码保持能力门控，本机没有执行或重新证明这些指令路径。
- 127 维短负载有负收益，第二个种子的长负载基本持平；大数据查询服务约慢 0.5%，不能宣称所有场景加速。
- Linux 6.5 memory.peak 不支持重置：capacity 为整段流程峰值，大数据比较只给完整试次可比较的 M/E。
- 2 GiB memory.max 始终设置且 swap=0，内核记录的 capacity 峰值高出上限 4096 字节，OOM/OOM kill 均为零。
- 大数据全量核对覆盖主键、类别、独立再生的范数，并非投影比较每个坐标；独立 TopK 在各事务/恢复状态另外验证。
- 本地性能结果受到共享服务器调度与 I/O 波动影响；保留逐轮数据、配对比值和负收益，不作正式分数预测。

最终二进制 SHA256：`d1bb32fdaf2e12ba0b5d02bd37d8c2ff072e611fa419994c4641ae652bf55541`；基线：`b190079fe993ad33a0a48006d167a41b98b12bba29c29248c1ae97c5522dd99d`。最终与已测 `remote-quad` 二进制字节相同（之后只修复两项测试断言）；95 个当前源码文件均与 final 快照哈希一致。`agent/conversation.json` SHA256 前后均为 `bb688d1bfc3e4d1af63603aa6d1aea6af35d3f2940406ea765355ddde6b4523f`，未伪造或修改。

- [完整复现命令](REPRODUCE.md)
- [机器可读结果](REMOTE_RESULTS.json)
- [本轮实现差异](REMOTE_IMPLEMENTATION.patch)
- [持续实验记录](REMOTE_NOTES.md)
- 原始 JSONL、stderr、逐轮 T/B/M、源码/二进制哈希、全部命令和不利结果位于 `results/`；历史成绩没有改写。
