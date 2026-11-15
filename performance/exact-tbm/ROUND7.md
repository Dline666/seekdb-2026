# ROUND7：受限内存下的 exact 派生索引

选定候选 `partial-balanced`；本报告只描述完成的本地结果。基线为 `4a257014c9f43fd1dbb86b008d32cd93303b4d94`，基线二进制 SHA-256 `b52c80e51e26b5edd4c30fe2d377115f34d4de02ca6dff1a38888e872b12e98b`。
选定二进制 SHA-256 `b190079fe993ad33a0a48006d167a41b98b12bba29c29248c1ae97c5522dd99d`。未正式提测；所有比较保留五轮及负例。
最终选定版本对 base 的大表服务合计（startup+cold+replay+shutdown；五轮中位数）见 [partial-balanced-large-summary.json](results/round7/partial-balanced-large-summary.json)：T 33.376481→23.216866 s（-30.44%），B 57149313024→38984933376 B（-31.78%），M 2147487744→2147581952 B（+94208 B）。M 仍在 2 GiB 配置附近，不能把 T/B 收益解释为降低峰值内存。
选定版本自身仍有负例（以下均为候选/基线的中位数之比变化）：[constant（4 MiB 索引预算）](results/round7/partial-balanced-budget4m-constant-summary.json)：查询 T +11.56%、查询 M +4.35%；[dim1024](results/round7/partial-balanced-dim1024-summary.json)：查询 T +4.63%、写入 T +8.25%。逐轮配对比及完整 T/B/M 见后表。
用户已授权本轮完成后，将分支 `perf/exact-round7` 推送到 https://github.com/Dline666/seekdb-2026、https://github.com/Wxtong-hub/seekdb-2026。本报告在提交和推送前生成，远端发布结果另以 Git 提交记录核实。

## 实现与取舍

最终保留 partial-balanced：仅包含预算内 partial 前缀及 balanced 分工；reuse、lean 均不进入生产补丁。
partial 将量化前缀按 128 行块限制在派生索引预算内，尾部始终走 exact 距离；分块误差上界只用于保守排除，不能改变排序真值。
预算同时计入数组 cap、slot map 的分配提示/历史高水位、量化数据和边界；map 成本仍为估算。删除后 free-list 扩容也必须重新检查预算，必要时禁用派生索引并回退 authoritative rows。
balanced 将已编码前缀和未编码尾部分别分给 worker，同一堆先扫 coded 再扫 tail；候选 slot 顺序不能证明递增时沿用原分区。
已撤下的 reuse 候选曾在已有 partial 索引的存活已编码 slot、同一完整映射内确认新旧 float32 向量字节相同后复用 codes/常数/保守界；新行、空槽复用、向量变化或映射不足仍重新编码。
bytes.Equal 额外读取旧向量 payload，在受限页缓存下可能增加 I/O。目标大数据更新的全部实测阶段未取得综合收益，不能仅凭小数据约 36% 的更新收益保留 reuse；这里未把具体退步全部归因于字节比较。
撤下 reuse 的目标场景证据：[partial-reuse-vs-balanced-large-update-summary.json](results/round7/partial-reuse-vs-balanced-large-update-summary.json) 中 balanced→reuse 的 UPDATE T 10.9631→11.1279 s，B 10.4247→10.1101 GB；全部实测阶段 T 34.5376→35.4236 s，B 49.1987→49.2595 GB。总 T 的中位数比/配对比变化为 +2.57%;+2.35%；完整 T/B/M 与两种比值见后表。
lean 将 worker 分支移出闭包，分配诊断确实少 96–103 B 和 1 alloc/query；但两组直接对照的查询时间中位数及本地 E 退步，因此未保留。1024 维的配对中位数与中位数之比方向不同，必须同时看。
原 partial 全测试曾通过，但后补删除预算复现失败：5272 > 5264 B、2568 > 2560 B；safe 对应测试通过。证据：[partial-delete-repro-tests.txt](results/round7/partial-delete-repro-tests.txt)、[partial-safe-delete-tests.txt](results/round7/partial-safe-delete-tests.txt)。
partial-safe 的完整 Go 测试也已通过（[partial-safe-tests.txt](results/round7/partial-safe-tests.txt)）；它是被 balanced 分工方案替代的正确性通过版本，不是因正确性失败被淘汰。最终候选必须通过自己的全部验证，不以 safe/reuse 的记录替代。

## 最终候选的功能与恢复门禁

报告重新核对以下日志的候选路径、实际命令、exit 0 和各自预期成功内容；缺少任何一项均拒绝生成。

| 检查 | 已验证范围 | 原始日志 |
|---|---|---|
| 完整 Go 测试 | engine / proto / sqlparse / types 全部通过；包含 partial/balanced 回归 | [partial-balanced-tests.txt](results/round7/partial-balanced-tests.txt) |
| 禁用 AVX-VNNI 的 engine 测试 | engine 通过；两个 AVX-VNNI 专项明确 unavailable/SKIP | [partial-balanced-fallback-tests.txt](results/round7/partial-balanced-fallback-tests.txt) |
| race | engine 与 types 均通过 | [partial-balanced-race.txt](results/round7/partial-balanced-race.txt) |
| 崩溃测试 | 30 轮通过；11874 个已提交行 | [partial-balanced-crash.txt](results/round7/partial-balanced-crash.txt) |
| exact 边界 | 8 个独立边界组合与 1 个浮点展开诊断 | [partial-balanced-exact-edges.txt](results/round7/partial-balanced-exact-edges.txt) |
| 协议/事务/存储 | 20 项逐项 PASS，包含重启、更新、提交与回滚 | [partial-balanced-protocol.txt](results/round7/partial-balanced-protocol.txt) |

硬件不支持的 AVX-512 路径及显式诊断测试仍按原测试套件跳过，完整 skip 名称保存在 summary.json。fallback 启动器 run.py 设置 MVDB_NO_AVXVNNI=1；历史日志没有环境变量快照，日志自身只能直接证明 AVX-VNNI 专项不可用且 engine 测试通过。
这些历史命令日志记录候选源码/二进制路径，未记录运行前后 SHA；不能把它们描述成各自具有哈希锁定。独立新 stress 另行验证选定二进制 SHA。

## 环境、计量与比值

WSL ArchBench / Intel Core Ultra 5 125H；Go 1.22.2、linux/amd64、GOAMD64=v1；2 CPU、2 GiB cgroup、无 swap，exact，fdatasync。环境身份见 [environment.json](results/round7/environment.json)。
T 单位秒，B 为 cgroup 实际读写字节，M 为 cgroup 内核峰值字节（包含文件缓存）。交错对照表为五轮中位数，Mmax 为五轮最大值；stress/capacity 表为单次实测阶段值，均不截断到配置上限。
变化列每项使用“中位数之比变化；逐轮候选/对照比的中位数变化”，正值代表该成本增加。零基准的比值为 —，原始零值仍保留。不同组不混合或重新加权。
普通 E 使用首三轮对照固定中位数、T/B/M=0.6/0.2/0.2、write/query=1/3、2/3，平滑量 1 ms/1 MiB/1 MiB；仅是同组本地诊断，不是平台分数。
普通查询 T 排除装载/首条预热，B/M 却覆盖整个进程；因此其查询时间不能单独代表首次派生索引成本。large 的 service_total 才包含启动、cold、replay、退出，T/B相加、M取最大。

## 选定版本参与的全部普通组

每行保留原始对照 → 候选方向，包含选定版本在任一侧的组；完整轮次及两种全部 T/B/M 比值见 [all-comparisons.json](results/round7/all-comparisons.json)。

| 组 / 阶段 | 对照 T/B/M | 候选 T/B/M | T变化：中位比；配对比 | E 对照→候选 |
|---|---:|---:|---:|---:|
| [partial-balanced-100k](results/round7/partial-balanced-100k-summary.json) / write（base→partial-balanced） | 3.082204 / 109711360 / 116387840 | 2.690938 / 109711360 / 116494336 | -12.69%;-13.81% | 0.497058→0.506247 |
| [partial-balanced-100k](results/round7/partial-balanced-100k-summary.json) / query（base→partial-balanced） | 0.505797 / 109711360 / 155488256 | 0.505419 / 109711360 / 155672576 | -0.07%;+0.62% | 0.497058→0.506247 |
| [partial-balanced-budget4m-constant](results/round7/partial-balanced-budget4m-constant-summary.json) / write（base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4） | 0.735367 / 41693184 / 53919744 | 0.757744 / 41693184 / 53739520 | +3.04%;+1.08% | 0.506046→0.493573 |
| [partial-balanced-budget4m-constant](results/round7/partial-balanced-budget4m-constant-summary.json) / query（base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4） | 0.737578 / 41693184 / 54280192 | 0.822858 / 41693184 / 56643584 | +11.56%;+11.56% | 0.506046→0.493573 |
| [partial-balanced-budget4m-outlier](results/round7/partial-balanced-budget4m-outlier-summary.json) / write（base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4） | 0.798130 / 41693184 / 53714944 | 0.779666 / 41693184 / 53616640 | -2.31%;-1.97% | 0.500913→0.518187 |
| [partial-balanced-budget4m-outlier](results/round7/partial-balanced-budget4m-outlier-summary.json) / query（base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4） | 0.709170 / 41693184 / 54136832 | 0.590172 / 41693184 / 56897536 | -16.78%;-17.40% | 0.500913→0.518187 |
| [partial-balanced-budget4m](results/round7/partial-balanced-budget4m-summary.json) / write（base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4） | 0.874827 / 41693184 / 54161408 | 0.853831 / 41693184 / 53923840 | -2.40%;+0.30% | 0.502549→0.518002 |
| [partial-balanced-budget4m](results/round7/partial-balanced-budget4m-summary.json) / query（base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4） | 1.459570 / 41693184 / 54202368 | 1.182776 / 41693184 / 57208832 | -18.96%;-16.83% | 0.502549→0.518002 |
| [partial-balanced-default](results/round7/partial-balanced-default-summary.json) / write（base→partial-balanced） | 0.211194 / 5853184 / 9932800 | 0.208107 / 5853184 / 10215424 | -1.46%;+4.90% | 0.512930→0.505995 |
| [partial-balanced-default](results/round7/partial-balanced-default-summary.json) / query（base→partial-balanced） | 0.098247 / 5853184 / 13160448 | 0.099620 / 5853184 / 12914688 | +1.40%;-0.00% | 0.512930→0.505995 |
| [partial-balanced-dim1024](results/round7/partial-balanced-dim1024-summary.json) / write（base→partial-balanced） | 0.800514 / 41693184 / 53624832 | 0.866544 / 41693184 / 53645312 | +8.25%;+2.97% | 0.498462→0.476658 |
| [partial-balanced-dim1024](results/round7/partial-balanced-dim1024-summary.json) / query（base→partial-balanced） | 0.448678 / 41693184 / 64217088 | 0.469462 / 41693184 / 64540672 | +4.63%;+3.36% | 0.498462→0.476658 |
| [partial-balanced-fallback128](results/round7/partial-balanced-fallback128-summary.json) / write（base:MVDB_NO_AVXVNNI=1→partial-balanced:MVDB_NO_AVXVNNI=1） | 0.199413 / 5853184 / 10272768 | 0.207290 / 5853184 / 10215424 | +3.95%;-0.23% | 0.499534→0.494992 |
| [partial-balanced-fallback128](results/round7/partial-balanced-fallback128-summary.json) / query（base:MVDB_NO_AVXVNNI=1→partial-balanced:MVDB_NO_AVXVNNI=1） | 0.341638 / 5853184 / 13238272 | 0.353242 / 5853184 / 12972032 | +3.40%;+4.12% | 0.499534→0.494992 |
| [partial-balanced-tail127](results/round7/partial-balanced-tail127-summary.json) / write（base→partial-balanced） | 0.252219 / 5812224 / 10240000 | 0.249625 / 5812224 / 10166272 | -1.03%;-1.03% | 0.492425→0.497148 |
| [partial-balanced-tail127](results/round7/partial-balanced-tail127-summary.json) / query（base→partial-balanced） | 0.163202 / 5812224 / 12857344 | 0.168062 / 5812224 / 12845056 | +2.98%;+3.17% | 0.492425→0.497148 |
| [partial-balanced-vs-safe-budget4m](results/round7/partial-balanced-vs-safe-budget4m-summary.json) / write（partial-safe:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4） | 1.099389 / 41693184 / 53858304 | 0.933012 / 41693184 / 53571584 | -15.13%;-13.04% | 0.507001→0.524831 |
| [partial-balanced-vs-safe-budget4m](results/round7/partial-balanced-vs-safe-budget4m-summary.json) / query（partial-safe:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4） | 1.405729 / 41693184 / 57126912 | 1.265951 / 41693184 / 57102336 | -9.94%;-11.93% | 0.507001→0.524831 |
| [partial-lean-vs-balanced-dim1024](results/round7/partial-lean-vs-balanced-dim1024-summary.json) / write（partial-balanced→partial-lean） | 0.768275 / 41693184 / 53432320 | 0.778414 / 41693184 / 53837824 | +1.32%;+2.44% | 0.499946→0.498919 |
| [partial-lean-vs-balanced-dim1024](results/round7/partial-lean-vs-balanced-dim1024-summary.json) / query（partial-balanced→partial-lean） | 1.274097 / 41693184 / 64385024 | 1.286129 / 41693184 / 63844352 | +0.94%;-0.12% | 0.499946→0.498919 |
| [partial-lean-vs-balanced-long](results/round7/partial-lean-vs-balanced-long-summary.json) / write（partial-balanced→partial-lean） | 0.203133 / 5853184 / 10342400 | 0.207499 / 5853184 / 10080256 | +2.15%;+1.62% | 0.499262→0.494512 |
| [partial-lean-vs-balanced-long](results/round7/partial-lean-vs-balanced-long-summary.json) / query（partial-balanced→partial-lean） | 1.450071 / 5853184 / 13479936 | 1.518613 / 5853184 / 13426688 | +4.73%;+2.04% | 0.499262→0.494512 |

## 全部普通候选与负例

没有因退步删除组或轮次。下表保留原对照名称；非 base 直接对照不能冒充基线对照。各指标退步轮编号完整保存在 JSON。

| 组：对照→候选 | write T 中位比；配对比 | query T 中位比；配对比 | query B 中位比；配对比 | query M 中位比；配对比 | E 对照→候选 |
|---|---:|---:|---:|---:|---:|
| [partial-balanced-100k](results/round7/partial-balanced-100k-summary.json)：base→partial-balanced | -12.69%;-13.81% | -0.07%;+0.62% | +0.00%;+0.00% | +0.12%;+0.23% | 0.497058→0.506247 |
| [partial-balanced-budget4m-constant](results/round7/partial-balanced-budget4m-constant-summary.json)：base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4 | +3.04%;+1.08% | +11.56%;+11.56% | +0.00%;+0.00% | +4.35%;+5.30% | 0.506046→0.493573 |
| [partial-balanced-budget4m-outlier](results/round7/partial-balanced-budget4m-outlier-summary.json)：base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4 | -2.31%;-1.97% | -16.78%;-17.40% | +0.00%;+0.00% | +5.10%;+5.10% | 0.500913→0.518187 |
| [partial-balanced-budget4m](results/round7/partial-balanced-budget4m-summary.json)：base:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4 | -2.40%;+0.30% | -18.96%;-16.83% | +0.00%;+0.00% | +5.55%;+5.06% | 0.502549→0.518002 |
| [partial-balanced-default](results/round7/partial-balanced-default-summary.json)：base→partial-balanced | -1.46%;+4.90% | +1.40%;-0.00% | +0.00%;+0.00% | -1.87%;-1.87% | 0.512930→0.505995 |
| [partial-balanced-dim1024](results/round7/partial-balanced-dim1024-summary.json)：base→partial-balanced | +8.25%;+2.97% | +4.63%;+3.36% | +0.00%;+0.00% | +0.50%;+0.50% | 0.498462→0.476658 |
| [partial-balanced-fallback128](results/round7/partial-balanced-fallback128-summary.json)：base:MVDB_NO_AVXVNNI=1→partial-balanced:MVDB_NO_AVXVNNI=1 | +3.95%;-0.23% | +3.40%;+4.12% | +0.00%;+0.00% | -2.01%;-0.85% | 0.499534→0.494992 |
| [partial-balanced-tail127](results/round7/partial-balanced-tail127-summary.json)：base→partial-balanced | -1.03%;-1.03% | +2.98%;+3.17% | +0.00%;+0.00% | -0.10%;+0.22% | 0.492425→0.497148 |
| [partial-balanced-vs-safe-budget4m](results/round7/partial-balanced-vs-safe-budget4m-summary.json)：partial-safe:MVDB_INDEX_MEM_MB=4→partial-balanced:MVDB_INDEX_MEM_MB=4 | -15.13%;-13.04% | -9.94%;-11.93% | +0.00%;+0.00% | -0.04%;+0.54% | 0.507001→0.524831 |
| [partial-lean-vs-balanced-dim1024](results/round7/partial-lean-vs-balanced-dim1024-summary.json)：partial-balanced→partial-lean | +1.32%;+2.44% | +0.94%;-0.12% | +0.00%;+0.00% | -0.84%;-0.76% | 0.499946→0.498919 |
| [partial-lean-vs-balanced-long](results/round7/partial-lean-vs-balanced-long-summary.json)：partial-balanced→partial-lean | +2.15%;+1.62% | +4.73%;+2.04% | +0.00%;+0.00% | -0.40%;+0.74% | 0.499262→0.494512 |
| [partial-reuse-100k](results/round7/partial-reuse-100k-summary.json)：base→partial-reuse | +3.43%;+3.43% | +10.90%;+12.37% | +0.00%;+0.00% | +0.05%;+0.05% | 0.503679→0.497911 |
| [partial-reuse-budget4m-constant](results/round7/partial-reuse-budget4m-constant-summary.json)：base:MVDB_INDEX_MEM_MB=4→partial-reuse:MVDB_INDEX_MEM_MB=4 | -3.92%;+0.31% | +8.34%;+5.19% | +0.00%;+0.00% | +3.37%;+3.75% | 0.497306→0.493105 |
| [partial-reuse-budget4m-outlier](results/round7/partial-reuse-budget4m-outlier-summary.json)：base:MVDB_INDEX_MEM_MB=4→partial-reuse:MVDB_INDEX_MEM_MB=4 | +0.72%;+0.80% | -15.91%;-15.77% | +0.00%;+0.00% | +5.25%;+5.14% | 0.499789→0.515612 |
| [partial-reuse-budget4m](results/round7/partial-reuse-budget4m-summary.json)：base:MVDB_INDEX_MEM_MB=4→partial-reuse:MVDB_INDEX_MEM_MB=4 | -5.53%;-8.56% | -18.56%;-16.90% | +0.00%;+0.00% | +5.11%;+4.23% | 0.503722→0.525507 |
| [partial-reuse-dim1024](results/round7/partial-reuse-dim1024-summary.json)：base→partial-reuse | -2.35%;+1.38% | -5.32%;-5.34% | +0.00%;+0.00% | -0.14%;-0.45% | 0.519899→0.532979 |
| [partial-reuse-fallback128](results/round7/partial-reuse-fallback128-summary.json)：base:MVDB_NO_AVXVNNI=1→partial-reuse:MVDB_NO_AVXVNNI=1 | +1.59%;+0.43% | -0.59%;+0.54% | +0.00%;+0.00% | -2.28%;-2.28% | 0.500097→0.498522 |
| [partial-reuse-long](results/round7/partial-reuse-long-summary.json)：base→partial-reuse | -5.82%;-5.82% | -2.85%;-1.02% | +0.00%;+0.00% | +2.31%;+1.93% | 0.489493→0.497611 |
| [partial-reuse-tail127](results/round7/partial-reuse-tail127-summary.json)：base→partial-reuse | -2.96%;-3.12% | +4.62%;+1.31% | +0.00%;+0.00% | +0.99%;+1.00% | 0.495598→0.494981 |
| [partial-safe-100k](results/round7/partial-safe-100k-summary.json)：base→partial-safe | +0.08%;-0.98% | -0.36%;-0.20% | +0.00%;+0.00% | -0.13%;-0.06% | 0.498359→0.497142 |
| [partial-safe-budget4m](results/round7/partial-safe-budget4m-summary.json)：base:MVDB_INDEX_MEM_MB=4→partial-safe:MVDB_INDEX_MEM_MB=4 | +0.50%;-1.33% | -9.06%;-13.75% | +0.00%;+0.00% | +5.16%;+5.25% | 0.506329→0.513480 |
| [partial-safe-default](results/round7/partial-safe-default-summary.json)：base→partial-safe | -3.60%;-3.81% | +4.62%;+1.82% | +0.00%;+0.00% | +2.10%;+0.20% | 0.500881→0.498726 |
| [partial-safe-dim1024](results/round7/partial-safe-dim1024-summary.json)：base→partial-safe | +1.07%;+3.53% | +0.88%;+2.07% | +0.00%;+0.00% | +0.19%;+0.49% | 0.500358→0.496845 |

## 全部大表交错组

fixture 最终有 549,997 行、1024 维、2,252,787,712 B 存活向量；所有存活类别均为 c9，WHERE c9 覆盖全表，不是 10% 选择率。
每次启动和 cold 批前验证私有日志冷页；cold 只指批开始时，批内可复用。replay 沿用同进程、派生状态和剩余页缓存，不意味着超过 2 GiB 的向量全部驻留。

| 组 / 阶段 | 对照→候选 T/B/M | ΔT / ΔB / ΔM（各：中位比；配对比） | Mmax 对照→候选 / 超配置字节 |
|---|---:|---:|---:|
| [partial-balanced-large](results/round7/partial-balanced-large-summary.json) / cold | 18.534450 / 30870798336 / 2147487744 → 15.685893 / 24160784384 / 2147487744 | -15.37%;-12.28% / -21.74%;-21.72% / +0.00%;+0.00% | 2147487744→2147635200 / 4096→151552 |
| [partial-balanced-large](results/round7/partial-balanced-large-summary.json) / replay | 12.558557 / 21975519232 / 2147487744 → 4.672455 / 10364932096 / 2147487744 | -62.79%;-62.13% / -52.83%;-53.09% / +0.00%;+0.00% | 2147741696→2147749888 / 258048→266240 |
| [partial-balanced-large](results/round7/partial-balanced-large-summary.json) / service_total | 33.376481 / 57149313024 / 2147487744 → 23.216866 / 38984933376 / 2147581952 | -30.44%;-31.95% / -31.78%;-31.78% / +0.00%;+0.00% | 2147741696→2147749888 / 258048→266240 |
| [partial-balanced-vs-safe-large](results/round7/partial-balanced-vs-safe-large-summary.json) / cold | 17.857437 / 25545453568 / 2147487744 → 16.541738 / 24203169792 / 2147487744 | -7.37%;-10.20% / -5.25%;-4.46% / +0.00%;+0.00% | 2147639296→2147487744 / 155648→4096 |
| [partial-balanced-vs-safe-large](results/round7/partial-balanced-vs-safe-large-summary.json) / replay | 5.175784 / 10105438208 / 2147487744 → 4.685129 / 10727911424 / 2147524608 | -9.48%;-9.48% / +6.16%;+4.56% / +0.00%;+0.00% | 2147487744→2147741696 / 4096→258048 |
| [partial-balanced-vs-safe-large](results/round7/partial-balanced-vs-safe-large-summary.json) / service_total | 26.064479 / 39912968192 / 2147487744 → 24.575722 / 39116509184 / 2147524608 | -5.71%;-6.15% / -2.00%;-2.66% / +0.00%;+0.00% | 2147639296→2147741696 / 155648→258048 |
| [partial-reuse-large](results/round7/partial-reuse-large-summary.json) / cold | 19.124746 / 30905176064 / 2147487744 → 16.841501 / 24149143552 / 2147487744 | -11.94%;-12.61% / -21.86%;-21.80% / +0.00%;+0.00% | 2147725312→2147663872 / 241664→180224 |
| [partial-reuse-large](results/round7/partial-reuse-large-summary.json) / replay | 12.860286 / 21965803520 / 2147487744 → 4.840936 / 10197594112 / 2147688448 | -62.36%;-64.22% / -53.58%;-53.69% / +0.01%;+0.00% | 2147639296→2147737600 / 155648→253952 |
| [partial-reuse-large](results/round7/partial-reuse-large-summary.json) / service_total | 35.307069 / 57192628224 / 2147504128 → 24.884727 / 38660710400 / 2147688448 | -29.52%;-31.56% / -32.40%;-32.35% / +0.01%;+0.00% | 2147725312→2147737600 / 241664→253952 |
| [partial-safe-large](results/round7/partial-safe-large-summary.json) / cold | 19.339157 / 30917009408 / 2147487744 → 17.999849 / 25427484672 / 2147504128 | -6.93%;-5.45% / -17.76%;-18.25% / +0.00%;+0.00% | 2147495936→2147741696 / 12288→258048 |
| [partial-safe-large](results/round7/partial-safe-large-summary.json) / replay | 13.746173 / 22094688256 / 2147487744 → 4.904374 / 9949163520 / 2147487744 | -64.32%;-64.30% / -54.97%;-57.05% / +0.00%;+0.00% | 2147487744→2147639296 / 4096→155648 |
| [partial-safe-large](results/round7/partial-safe-large-summary.json) / service_total | 35.348392 / 57264369664 / 2147487744 → 25.693297 / 39690620928 / 2147639296 | -27.31%;-27.83% / -30.69%;-31.85% / +0.01%;+0.01% | 2147495936→2147741696 / 12288→258048 |

large 的 startup/shutdown 及所有原始轮次同样保存在 JSON；每组 120 条独立响应检查、6 个唯一 SQL oracle，不能把重复响应数当成 120 个独立查询案例。

## 建索引后的更新与恢复

各组均为五轮交错、10,000×1024、4 MiB 索引预算。先执行 index_query，再做 category/front/tail 更新；ACK 包含 fdatasync 与同步索引维护。
每组 180 条 TopK 响应、12 个状态/SQL oracle、20 次 census、200,000 行检查。全部 13 个实测阶段与两个合计保存在 JSON；下表保留主要阶段及全部三组对照。

| 组 / 阶段 | 对照→候选 T/B/M | ΔT / ΔB / ΔM（各：中位比；配对比） |
|---|---:|---:|
| [partial-balanced-update](results/round7/partial-balanced-update-summary.json) / index_query | 0.016608 / 0 / 48410624 → 0.062131 / 0 / 51154944 | +274.11%;+275.69% / —;— / +5.67%;+5.79% |
| [partial-balanced-update](results/round7/partial-balanced-update-summary.json) / updated_query | 0.012465 / 0 / 96309248 → 0.011210 / 0 / 103317504 | -10.07%;-12.20% / —;— / +7.28%;+6.98% |
| [partial-balanced-update](results/round7/partial-balanced-update-summary.json) / recovery | 0.045953 / 82571264 / 87912448 → 0.043756 / 82571264 / 88076288 | -4.78%;+1.96% / +0.00%;+0.00% / +0.19%;+0.40% |
| [partial-balanced-update](results/round7/partial-balanced-update-summary.json) / recovered_query | 0.015705 / 0 / 87871488 → 0.058249 / 0 / 90296320 | +270.88%;+289.64% / —;— / +2.76%;+3.10% |
| [partial-balanced-update](results/round7/partial-balanced-update-summary.json) / indexed_update_ack_total | 0.161115 / 41299968 / 96595968 → 0.179349 / 41299968 / 103620608 | +11.32%;+25.56% / +0.00%;+0.00% / +7.27%;+6.93% |
| [partial-balanced-update](results/round7/partial-balanced-update-summary.json) / measured_stage_sum | 1.333175 / 165797888 / 101421056 → 1.348938 / 165797888 / 107458560 | +1.18%;+14.34% / +0.00%;+0.00% / +5.95%;+3.84% |
| [partial-reuse-update](results/round7/partial-reuse-update-summary.json) / index_query | 0.016292 / 0 / 48181248 → 0.058739 / 0 / 51245056 | +260.54%;+264.51% / —;— / +6.36%;+6.36% |
| [partial-reuse-update](results/round7/partial-reuse-update-summary.json) / updated_query | 0.012662 / 0 / 96571392 → 0.010767 / 0 / 100794368 | -14.97%;-14.97% / —;— / +4.37%;+4.37% |
| [partial-reuse-update](results/round7/partial-reuse-update-summary.json) / recovery | 0.034833 / 82571264 / 88113152 → 0.035193 / 82571264 / 88113152 | +1.03%;-2.10% / +0.00%;+0.00% / +0.00%;-0.11% |
| [partial-reuse-update](results/round7/partial-reuse-update-summary.json) / recovered_query | 0.015248 / 0 / 88113152 → 0.057151 / 0 / 90865664 | +274.80%;+286.52% / —;— / +3.12%;+3.18% |
| [partial-reuse-update](results/round7/partial-reuse-update-summary.json) / indexed_update_ack_total | 0.058349 / 41299968 / 96980992 → 0.060332 / 41299968 / 100794368 | +3.40%;+3.74% / +0.00%;+0.00% / +3.93%;+4.37% |
| [partial-reuse-update](results/round7/partial-reuse-update-summary.json) / measured_stage_sum | 1.003307 / 165797888 / 100802560 → 1.059682 / 165797888 / 105193472 | +5.62%;+7.64% / +0.00%;+0.00% / +4.36%;+4.47% |
| [partial-reuse-vs-balanced-update](results/round7/partial-reuse-vs-balanced-update-summary.json) / index_query | 0.058998 / 0 / 51036160 → 0.060635 / 0 / 51253248 | +2.78%;+2.78% / —;— / +0.43%;+0.43% |
| [partial-reuse-vs-balanced-update](results/round7/partial-reuse-vs-balanced-update-summary.json) / updated_query | 0.010621 / 0 / 100782080 → 0.011369 / 0 / 100847616 | +7.04%;+7.47% / —;— / +0.07%;+0.69% |
| [partial-reuse-vs-balanced-update](results/round7/partial-reuse-vs-balanced-update-summary.json) / recovery | 0.036817 / 82571264 / 87785472 → 0.041073 / 82571264 / 87949312 | +11.56%;+7.69% / +0.00%;+0.00% / +0.19%;-0.03% |
| [partial-reuse-vs-balanced-update](results/round7/partial-reuse-vs-balanced-update-summary.json) / recovered_query | 0.057540 / 0 / 90648576 → 0.057324 / 0 / 90894336 | -0.37%;+0.54% / —;— / +0.27%;+0.23% |
| [partial-reuse-vs-balanced-update](results/round7/partial-reuse-vs-balanced-update-summary.json) / indexed_update_ack_total | 0.108635 / 41299968 / 101437440 → 0.069438 / 41299968 / 100847616 | -36.08%;-35.59% / +0.00%;+0.00% / -0.58%;-0.71% |
| [partial-reuse-vs-balanced-update](results/round7/partial-reuse-vs-balanced-update-summary.json) / measured_stage_sum | 1.117596 / 165797888 / 105205760 → 1.087965 / 165797888 / 105246720 | -2.65%;-1.59% / +0.00%;+0.00% / +0.04%;-0.39% |

更新组不是 >2GiB 容量测试。恢复前私有日志 sync/驱逐并验证冷页；初始/更新真值独立生成，恢复查询复用更新真值。measured_stage_sum 为 13 阶段 T/B 和、M 最大值，不构造加权 E。

## 大数据索引更新直接对照

550,000×1024 初始行、549,997 存活行，2 GiB / 无 swap。每轮 pre_update 建索引，再将全部 c9 改为 c8，随后 post_update 与 census；比较受限页缓存下复用量化与额外旧 payload 读取的综合成本。
每组五轮 AB/BA，120 条 TopK 响应、12 个状态/SQL oracle、10 次 census / 5,499,970 行检查。全部七个 scope 如下，既保留更新 ACK，也保留首次构建、查询及服务合计；不计算跨阶段 E。

| 组 / 阶段 | 对照→候选 T/B/M | ΔT / ΔB / ΔM（各：中位比；配对比） | Mmax 对照→候选 / 超配置字节 |
|---|---:|---:|---:|
| [partial-reuse-vs-balanced-large-update](results/round7/partial-reuse-vs-balanced-large-update-summary.json) / startup | 1.586136 / 4313972736 / 2147483648 → 1.639575 / 4313972736 / 2147483648 | +3.37%;+3.37% / +0.00%;+0.00% / +0.00%;+0.00% | 2147483648→2147483648 / 0→0 |
| [partial-reuse-vs-balanced-large-update](results/round7/partial-reuse-vs-balanced-large-update-summary.json) / pre_update | 14.609072 / 24069300224 / 2147487744 → 15.103568 / 24105570304 / 2147487744 | +3.38%;+3.24% / +0.15%;+0.20% / +0.00%;+0.00% | 2147733504→2147737600 / 249856→253952 |
| [partial-reuse-vs-balanced-large-update](results/round7/partial-reuse-vs-balanced-large-update-summary.json) / category_update | 10.963115 / 10424729600 / 2147483648 → 11.127894 / 10110058496 / 2147487744 | +1.50%;+4.76% / -3.02%;-4.59% / +0.00%;+0.00% | 2147487744→2147487744 / 4096→4096 |
| [partial-reuse-vs-balanced-large-update](results/round7/partial-reuse-vs-balanced-large-update-summary.json) / post_update | 3.536818 / 9329975296 / 2147487744 → 3.670822 / 9523974144 / 2147487744 | +3.79%;+2.58% / +2.08%;+4.14% / +0.00%;+0.00% | 2147655680→2147487744 / 172032→4096 |
| [partial-reuse-vs-balanced-large-update](results/round7/partial-reuse-vs-balanced-large-update-summary.json) / census | 3.729546 / 1333792768 / 2147483648 → 3.615273 / 1212092416 / 2147483648 | -3.06%;-2.75% / -9.12%;-3.55% / +0.00%;+0.00% | 2147483648→2147483648 / 0→0 |
| [partial-reuse-vs-balanced-large-update](results/round7/partial-reuse-vs-balanced-large-update-summary.json) / shutdown | 0.030200 / 0 / 2138816512 → 0.026682 / 0 / 2123649024 | -11.65%;-11.65% / —;— / -0.71%;-0.35% | 2140282880→2139500544 / 0→0 |
| [partial-reuse-vs-balanced-large-update](results/round7/partial-reuse-vs-balanced-large-update-summary.json) / measured_stage_sum | 34.537620 / 49198727168 / 2147487744 → 35.423602 / 49259458560 / 2147487744 | +2.57%;+2.35% / +0.12%;-0.06% / +0.00%;+0.00% | 2147733504→2147737600 / 249856→253952 |

v2 将私有文件的首次 streaming copy+Sync 和后续 truncate+Sync 放在数据库同一 cgroup，校验 producer/reset/实测阶段归属；UPDATE 必须实际追加至少存活向量字节数，物理写入至少覆盖 log growth 减一页（最低 1 B），无等待落账。
每轮数据库退出后核对原始 prefix / 全日志哈希，再 truncate+Sync 复原私有日志并驱逐缓存；这些复位、哈希和外部真值成本排除在阶段 T/B/M 外，并保留原始复位证明。UPDATE T 包含 ACK 后长度检查，不隐藏写回等待；该对照不替代 fresh-write/crash stress。

## 选定版本的重新写入与崩溃恢复

[partial-balanced-stress-recovered-summary.json](results/round7/partial-balanced-stress-recovered-summary.json) 与原始日志通过门禁：新写入 550,000×1024，初始向量 2,252,800,000 B > 2 GiB；最终存活 549,997 行。
实际校验 73 条 TopK（6×12+1）、2 次完整 census / 1,099,994 行、6 次事务点行检查；4 次 SIGKILL，包含已提交后、未提交尾部、恢复未 ready、bulk commit 后。
未提交尾部 8285 B 已验证丢弃。全流程峰值 2147717120 B，超过配置 233472 B；OOM/oom_kill 为零。

本次成功压力测试采用独立重跑标签 `partial-balanced-stress-recovered`。此前中断尝试的原始工件（包括空或不完整 JSON）按原字节保存在 [failed-attempts 归档](results/round7/failed-attempts/)，每个文件的长度及 SHA-256 保存在 summary.json；环境故障与恢复记录见 [stress-environment-failure.json](results/round7/stress-environment-failure.json)。
恢复重跑的存储环境与原交错性能对照不同；该次重跑仅用于容量、exact、事务与崩溃恢复正确性检查，其阶段 T/B/M 不与原对照混比。

| 实测阶段（含重复 census） | T / B / M | M 超配置字节 |
|---|---:|---:|
| initial_startup | 0.002104 / 0 / 1835008 | 0 |
| write | 40.574439 / 2305736704 / 2147483648 | 0 |
| query_after_write | 25.382414 / 4839600128 / 2147483648 | 0 |
| updates_deletes_rollback | 0.029808 / 159744 / 2147434496 | 0 |
| query_after_updates | 12.268061 / 9749442560 / 2147487744 | 4096 |
| kill_after_acknowledged_commits | 0.029067 / 0 / 2147254272 | 0 |
| recovery_after_commit_kill | 0.658938 / 412647424 / 2147483648 | 0 |
| query_after_commit_recovery | 10.622919 / 5043310592 / 2147487744 | 4096 |
| uncommitted_tail_flushed | 0.184259 / 2424832 / 2147483648 | 0 |
| kill_with_uncommitted_transaction | 0.040563 / 0 / 2146263040 | 0 |
| kill_during_recovery | 0.011647 / 15548416 / 1888387072 | 0 |
| final_recovery | 0.916287 / 987639808 / 2147483648 | 0 |
| query_after_final_recovery | 11.512798 / 14086168576 / 2147487744 | 4096 |
| final_full_census | 4.391045 / 1344974848 / 2147487744 | 4096 |
| bulk_update | 12.252829 / 6000103424 / 2147483648 | 0 |
| query_after_bulk_update | 17.773498 / 38746140672 / 2147622912 | 139264 |
| kill_after_bulk_commit | 0.051815 / 0 / 2147426304 | 0 |
| recovery_after_bulk_commit | 2.176308 / 3367759872 / 2147483648 | 0 |
| query_after_bulk_recovery | 19.366468 / 30213029888 / 2147717120 | 233472 |
| final_full_census | 5.516883 / 3169337344 / 2147487744 | 4096 |
| graceful_shutdown | 0.060948 / 0 / 2097303552 | 0 |

旧日志容量检查另行通过：[partial-balanced-capacity-summary.json](results/round7/partial-balanced-capacity-summary.json)，point/order/census 各 1 条 SQL，共 3 条 SQL / 550,008 行；census 为 549,997 存活行。不是 6 条 SQL，validation 记录不重复计数。
该 capacity 元数据只有二进制路径，没有运行前后 SHA；不能宣称该旧驱动自身提供哈希锁定。新 stress 的候选 SHA 与本次保存的二进制 manifest 一致。
此前 partial-reuse-capacity 的私有目录与大表对照撞名，驱动在启动数据库前以 exit 2 拒绝，JSONL 为空；这是运行脚本命名问题，不是数据库查询/恢复失败。[partial-reuse-capacity-run.json](results/round7/partial-reuse-capacity-run.json)、[partial-reuse-capacity.stderr.txt](results/round7/partial-reuse-capacity.stderr.txt) 均保留；独立目录重跑 [partial-reuse-capacity-fixed-summary.json](results/round7/partial-reuse-capacity-fixed-summary.json) 已通过。最终 balanced 使用自己的默认 capacity 标签，不用 reuse 的结果替代最终验证。

| capacity 阶段 | T / B / M | M 超配置字节 |
|---|---:|---:|
| point_startup | 1.801377 / 4313972736 / 2147483648 | 0 |
| point_query | 0.003891 / 4202496 / 52563968 | 0 |
| point_shutdown | 0.000418 / 0 / 52563968 | 0 |
| order_startup | 1.530159 / 4313972736 / 2147483648 | 0 |
| order_query | 0.111266 / 16777216 / 60964864 | 0 |
| order_shutdown | 0.002270 / 0 / 60964864 | 0 |
| census_startup | 1.601449 / 4313972736 / 2147483648 | 0 |
| census_query | 8.635599 / 4342439936 / 2147483648 | 0 |
| census_shutdown | 0.026830 / 0 / 2118840320 | 0 |

## 内存定位与预算

按 N=549997、D=1024、预算 268435456 B：headroom=16777216，slot估计=35199808，scratch=4096；每 128 行块 135184 B，可容纳 1601 块。
规划 Q=204928 行（37.25983960%），量化+scratch=216433680 B，eq cache 前总估计=251633488 B，余量=16801968 B；全量量化估计=616069488 B。
B/16 是未分配余量，不是受保护 reservation 或 RSS 硬保证。删除、map 高水位和数组容量检查限制派生状态，不能承诺整个进程峰值不超过 2 GiB。
partial-safe 已完成对照中，查询 anon 独立最大值约 58 MB→275 MB，量级接近约 216 MB 量化状态；启动已填满文件缓存，anon 增长挤占 file，所以 B 下降而 M 仍接近上限。不能将 anon 当成 Go live heap，未做 heap profile。
[partial-memory-analysis.md](results/round7/partial-memory-analysis.md) 仅对应 partial-safe-large，绝不是最终 reuse/balanced 压力结果。50 ms 同记录字段也由顺序读取组成，非原子；独立 anon/file 最大值不可相加，file_mapped 是 file 子集，RSS 与映射缓存重叠。
内核 M 与采样 memory.current 的超限值分别保存在 all-comparisons/summary JSON，不把采样拼成 memory.peak 瞬间，也不把数值钳制到配置上限；缺少采样时记 null，不能记作零超限。

## 功能 smoke 与证据边界

以下只验证驱动/校验器功能；相同二进制或明确 smoke 数据不进入任何优化结论。早期 smoke 依赖当时摘要，不追溯声称通过后来加固的身份校验。

| 功能验证 | 类型 | SHA 是否相同 | 原摘要真值数 | 校验范围 |
|---|---|---|---:|---|
| [large-driver-smoke-summary.json](results/round7/large-driver-smoke-summary.json) | large | True | 120 | 历史功能验证 |
| [large-update-driver-smoke-summary.json](results/round7/large-update-driver-smoke-summary.json) | large_update | True | 120 | 历史：写回归属无效 |
| [large-update-driver-smoke-v2-summary.json](results/round7/large-update-driver-smoke-v2-summary.json) | large_update | True | 120 | 当前加固功能验证 |
| [update-driver-smoke-summary.json](results/round7/update-driver-smoke-summary.json) | update | True | 180 | 历史功能验证 |
| [update-driver-smoke-v2-summary.json](results/round7/update-driver-smoke-v2-summary.json) | update | True | 180 | 当前加固功能验证 |

v1 update smoke 对应 [update_run_initial.py](results/round7/update_run_initial.py) / [update_analyze_initial.py](results/round7/update_analyze_initial.py)；只有 v2 smoke 与 [update-validator-selfcheck-v2.json](results/round7/update-validator-selfcheck-v2.json) 的 18 个案例（1 个有效副本 + 17 个损坏拒绝，含一致伪造 TopK）对应当前加固流程。它们不是候选性能/正确性测试。
large 初期 wrapper 保存在 [large_run_initial.py](results/round7/large_run_initial.py)；后来增加 --baseline，不表示 driver 源码或二进制变更。
large_update v1 smoke 的写回 cgroup 归属错误，不能用其成功摘要证明物理写入计量正确；初始 driver/analyzer 保存在 [large_update_initial.go](results/round7/large_update_initial.go) / [large_update_analyze_initial.py](results/round7/large_update_analyze_initial.py)。
large_update v2 smoke 已经当前分析器重验：copy/reset 同 cgroup、growth 对应物理写入下界；10 次 UPDATE 均写入 258,048 B。[large-update-validator-selfcheck.json](results/round7/large-update-validator-selfcheck.json) 的 29 项全部通过（1 个有效副本 + 28 个损坏记录拒绝）；这是功能/校验器自检，不是 28 次数据库故障注入或候选性能证据。
census 对比主键、类别和独立再生的范数，未投影比较每个向量坐标。恢复中 SIGKILL 证明发生在 ready 前，尚无引擎内部恢复进度 hook。
所有阶段均排除外部 oracle 生成/解码/哈希/显式驱逐等准备工作；stress 写入 T 排除生成器；没有每请求 deadline，容量通过不等同正式时限通过。普通、large、update、stress 的计量范围不能混算。
用户提供的正式 4100 分缺对应提交 SHA、平台 CPU 和分项 T/B/M。现有本地 E 和容量结果无法可靠换算成 5500，也不能据此上调此前粗估；本轮未消耗正式提测次数。
可机读证据：[summary.json](results/round7/summary.json)、[all-comparisons.json](results/round7/all-comparisons.json)。所有阶段、逐轮配对比、失败测试、超配置峰值与输入 SHA-256 均保留。
