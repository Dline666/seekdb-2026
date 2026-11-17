# 本轮复现命令

在本服务器顺序执行，禁止同时运行另一组性能测试。每个标签、数据目录和构建快照必须新建；工具用排他创建保护既有结果。完整实际 argv、时间、退出码和二进制身份见 `results/commands.jsonl`、每次 `*-run.json`。未使用正式提测服务。

```bash
cd /home/platemo/seekdb-experiments/20261117/repository
export MVDB_LAB_ROOT=/home/platemo/seekdb-experiments/20261117/experiment-runtime
export MVDB_LAB_GO=/home/platemo/seekdb-experiments/20261117/toolchains/go/bin/go
export GOTOOLCHAIN=local GOPROXY=off GOAMD64=v1 CGO_ENABLED=0
export GOMAXPROCS=2 GOGC=20 MVDB_SYNC=fdatasync
export ADV_LAB="$PWD/performance/advanced-tbm-20261117"
export ADV_PY=/home/platemo/.local/bin/python3
export ADV_LINT="$MVDB_LAB_ROOT/lint/golangci-lint-2.15.0-linux-amd64/golangci-lint"
test -f /home/platemo/seekdb-experiments/20261117/MIGRATION_COMPLETE.json
"$MVDB_LAB_GO" version
"$ADV_LINT" version
```

编译、编辑、lint 均用 platemo；创建 cgroup 的 driver 必须 sudo -n。硬限制 2 CPU、2 GiB、swap=0，数据库 CPU 16–17、外部真值 driver CPU 18，fdatasync。数据设备的分区号为 259:4，沿 sysfs 映射到可计量的物理设备 259:0。不执行整机 drop_caches；大数据比较只驱逐新建私有 inode 并用 mincore 检查。

## 固定版本

- 上轮 `remote-final`：`d1bb32fdaf2e12ba0b5d02bd37d8c2ff072e611fa419994c4641ae652bf55541`。
- 本轮 `advanced-final`：`4fb46d9c21d40092c0b12e6be3f3cd8bcef012351a72511539e65481ce68f178`。
- 原 Round7：`remote-base`，身份保留在旧目录及本轮对照元数据。

上述源码在 `$MVDB_LAB_ROOT/版本名`，二进制在其 `bin/版本名`。`sources/` 保存相对上轮的补丁和资料清单，构建时的逐文件 SHA256 在 `results/*-source.json`。所有失败版本也保留。Go 固定 1.22.2，PGO 显式关闭；未把 lint 自身使用的编译器版本当成数据库工具链。

```bash
"$ADV_PY" "$ADV_LAB/lab.py" build replay-final --source "$MVDB_LAB_ROOT/advanced-final"
sha256sum "$MVDB_LAB_ROOT/bin/replay-final"
"$ADV_PY" "$ADV_LAB/lab.py" run replay-tests -- "$MVDB_LAB_GO" -C "$MVDB_LAB_ROOT/replay-final" test ./... -count=1
"$ADV_PY" "$ADV_LAB/lab.py" run replay-fallback -- env MVDB_NO_SIMD=1 "$MVDB_LAB_GO" -C "$MVDB_LAB_ROOT/replay-final" test ./internal/engine -count=1
"$ADV_PY" "$ADV_LAB/lab.py" run replay-race -- env CGO_ENABLED=1 "$MVDB_LAB_GO" -C "$MVDB_LAB_ROOT/replay-final" test -race ./internal/engine ./internal/proto ./internal/sqlparse -count=1
"$ADV_PY" "$ADV_LAB/lab.py" run replay-lint -- make -C "$MVDB_LAB_ROOT/replay-final" lint "GOLANGCI_LINT=$ADV_LINT"
```

如修改源码，先在可编辑副本 `make fmt`，再建立新快照并重测。不要对保存的旧快照重新格式化。平台构建使用根目录实际 Dockerfile，`build_docker.py` 提取镜像二进制并要求与原生 SHA256 完全相同。

## 五轮 T/B/M 矩阵

```bash
# 已保留的驱动为 bin/advanced-tbm；源为本目录 driver.go。
bash "$ADV_LAB/final_matrix.sh" replay
# 单组示例：每组五轮 AB/BA，两个版本共享独立 oracle。
sudo -n taskset --cpu-list 18 "$ADV_PY" "$ADV_LAB/lab.py" bench replay-one \
  --bins remote-final,advanced-final -rows 10000 -dim 128 -queries 1000 -seed 51117
# 编码器同二进制开关，避免把代码布局等构建差异当成编码收益。
sudo -n taskset --cpu-list 18 "$ADV_PY" "$ADV_LAB/lab.py" bench replay-encoder \
  --bins advanced-final:MVDB_NO_QUANTIZE_AVX2=1,advanced-final \
  -rows 100000 -dim 256 -queries 2000 -seed 51119
```

矩阵包含两个默认种子、长序列、10 万行、127/1024 维、1%/10%/100% 候选选择率、常数、离群、多行转义、4 MiB 部分索引、原 Round7 和实际 Docker。普通评分对照要求逐查询真值及全响应哈希一致；标量回退的末位浮点差异由独立 audit 验证，单列为正确性检查。

原始 `t_s` 保留旧阶段口径。主分析另外计入查询准备、启动、退出及最终文件同步；完整重复装载也计入的口径另列为敏感性分析。B/M 一直覆盖完整装载、构建、预热及最终写回。不要把短阶段 memory.peak 当作可重置的独立峰值。

## 超内存、事务和恢复

```bash
# 编译为 platemo；仅执行 cgroup driver 时提权。
"$MVDB_LAB_GO" build -trimpath -o "$MVDB_LAB_ROOT/bin/advanced-stress" "$ADV_LAB/stress.go"
sudo -n taskset --cpu-list 18 "$ADV_PY" "$ADV_LAB/stress_run.py" advanced-final --stress-label replay-capacity
sudo -n taskset --cpu-list 18 "$ADV_PY" "$ADV_LAB/large_run.py" replay-large advanced-final \
  --baseline remote-final --queries 12 \
  --fixture "$MVDB_LAB_ROOT/remote-final-stress-v2/db/data.log"
```

capacity 为 550,000×1024 float32，原始向量 2,252,800,000 字节，超过 2 GiB。独立 oracle 在计时之外；四次 SIGKILL、事务覆盖、两次全量 census、重放及幂等性均留原始输出。新增 tail audit 独立验证真正写进磁盘的三个记录的 CRC、主键和全部向量坐标，零填充不能冒充“未提交记录已写盘”。Linux 6.5 memory.peak 无法重置，capacity 只报完整流程 M；大数据对照每次新 cgroup，只为完整 service_total 报可比较 M/E。该 E 是查询服务比较，不能冒充完整写入/查询总 E。

本次的完整验证次序与实际参数见 `final_validate.sh`，其中包括小规模 tail audit 自检、解析式浮点/排序边界、原生和实际 Docker 的公开协议用例、三个种子各 40 次随机 crash、五轮 SIMD/标量独立真值，以及上述容量和超内存比较。脚本里的 `final-*` 标签已经存在，应复制脚本并统一换新标签后复现，不能覆盖。

```bash
# 原生公开协议：新 output 目录必须不存在。
"$ADV_PY" "$ADV_LAB/lab.py" run replay-protocol -- "$ADV_PY" protocol_check/run.py \
  --command '["/home/platemo/seekdb-experiments/20261117/experiment-runtime/bin/advanced-final","--data-dir","{data_dir}"]' \
  --output "$ADV_LAB/results/replay-public-native"
"$ADV_PY" "$ADV_LAB/lab.py" run replay-protocol-docker -- "$ADV_PY" protocol_check/run.py \
  --image seekdb-advanced-20261117:advanced-final --output "$ADV_LAB/results/replay-public-docker"
"$ADV_PY" "$ADV_LAB/lab.py" run replay-edges -- "$ADV_PY" performance/exact-tbm/exact_edges.py \
  --binary advanced-final --bin-dir "$MVDB_LAB_ROOT/bin" --assert-correct
for adv_seed in 1 2 3; do
  "$ADV_PY" "$ADV_LAB/lab.py" run "replay-crash-$adv_seed" -- env "MVDB_TMP=$MVDB_LAB_ROOT" \
    "$ADV_PY" minivectordb/tools/crash_test.py --bin "$MVDB_LAB_ROOT/bin/advanced-final" --rounds 40 --seed "$adv_seed"
done
```

## 剖析（禁止与计分实验并发）

`make_profile.py` 在单独快照注入分阶段 CPU/heap/runtime/GC/rusage 采样；这些二进制绝不计分。`perfwrap.c` 通过 perf_event_open 仅计数被测子进程及其线程，不修改 perf_event_paranoid。

```bash
"$ADV_PY" "$ADV_LAB/make_profile.py" "$MVDB_LAB_ROOT/advanced-final" replay-profile-source
"$ADV_PY" "$ADV_LAB/lab.py" build replay-profile --source "$MVDB_LAB_ROOT/replay-profile-source"
sudo -n taskset --cpu-list 18 "$ADV_PY" "$ADV_LAB/lab.py" bench replay-profile-default \
  --bins replay-profile --rounds 1 -rows 10000 -dim 128 -queries 2000 -seed 11317 \
  -diagnostic-repeat 20 -diagnostic-dir "$ADV_LAB/profiles/replay-default"
"$MVDB_LAB_GO" tool pprof -top "$MVDB_LAB_ROOT/bin/replay-profile" \
  "$ADV_LAB/profiles/replay-default/round0-replay-profile-query/query.cpu"
```

硬件计数附带 multiplex enabled/running 时间；software 事件应使用两种模式，首次用户态限定的 context-switch=0 结果无诊断意义。所有外部资料 URL、日期与适用版本见 `sources/references.json` 和 `RESEARCH.md`。

最终实际剖析与编译器逃逸/BCE 命令完整保存在 `final_diagnostics.sh`；原始与最终同种子比较由 `profile_summary.py` 汇总。最终又用非采样二进制补跑 100k 长序列，已纳入 `final_matrix.sh` 的第 17 组（seed=41151），不是把诊断运行当作成绩。

## 源码、结果与独立重算

`sources/FINAL_IMPLEMENTATION.patch` 是相对上一轮完整快照的补丁，路径以仓库根为基准。`sources/CANDIDATE_SOURCES.tar.gz` 含本轮所有构建快照及失败实验源码，`CANDIDATE_SOURCES.json` 列出逐文件哈希。它们不是 Git HEAD 的补丁：最初工作树本来含有上一轮尚未提交的改动。

`summarize_final.py` 从每组保留的原始五轮样本重算同轮配对 E，基线每轮为 0.5，并且保留原先前三轮中位数锚点结果。它严格要求全部最终组、容量和验证记录存在且通过，使用排他写入：

```bash
"$ADV_PY" "$ADV_LAB/summarize_final.py" --output "$ADV_LAB/RESULTS-recomputed.json"
"$ADV_PY" "$ADV_LAB/write_report.py" --output "$ADV_LAB/REPORT-recomputed.md"
```

结果 JSON 包含六项 T/B/M 的全部样本、准备/启动/退出分项、成对变化、中位数/MAD/范围、三种口径的 E、失败记录和完整容量记录。最终分析没有删除离群轮次，没有增加跨负载的任意权重。旧结果及 `agent/conversation.json` 的保护哈希对照见 `results/protected-before.json` 与 `protected-after.json`。

`audit_delivery.py` 独立使用 Decimal 和原始 ratio 公式复核 255 个逐轮 E，检查 340 个阶段的 cgroup/同步/I/O 约束，在临时副本实际应用最终补丁并逐文件比对快照，重新生成报告核对字节一致，再检查 3,830 个候选归档文件及 352 个受保护旧文件。已执行的通过记录为 `results/delivery-audit.json`；再次执行须更换其输出标签。`ARTIFACTS.json` 给出本轮交付文件的最终哈希。
