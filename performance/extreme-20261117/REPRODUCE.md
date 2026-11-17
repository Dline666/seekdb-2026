# 远端实验复现

所有命令在 `/home/platemo/seekdb-experiments/20261117/repository` 运行。原始命令还保存在 `results/remote-commands.jsonl` 和每次实验的 `*-run.json`，其参数为实际执行参数。既有结果名不允许覆盖；以下使用新的 `replay` 前缀，重复复现时再次更换前缀。顺序执行，不同时运行另一组测试或性能测量。

## 环境及不可变输入

```bash
cd /home/platemo/seekdb-experiments/20261117/repository
export MVDB_LAB_ROOT=/home/platemo/seekdb-experiments/20261117/experiment-runtime
export MVDB_LAB_GO=/home/platemo/seekdb-experiments/20261117/toolchains/go/bin/go
export PATH="$(dirname "$MVDB_LAB_GO"):$PATH"
export GOTOOLCHAIN=local GOPROXY=off GOAMD64=v1 CGO_ENABLED=0
export GOMAXPROCS=2 GOGC=20 MVDB_SYNC=fdatasync
export LAB_PY=/home/platemo/.local/bin/python3
export LAB_DIR="$PWD/performance/extreme-20261117"
export LAB_LINT="$MVDB_LAB_ROOT/lint/golangci-lint-2.15.0-linux-amd64/golangci-lint"
test -f /home/platemo/seekdb-experiments/20261117/MIGRATION_COMPLETE.json
"$MVDB_LAB_GO" version
"$LAB_LINT" version
```

数据库编译固定 Go 1.22.2，`GOAMD64=v1`，实际 SIMD 由运行时 CPUID/OS 检查。源码、快照、编译和 Codex 由 platemo 操作；只有 cgroup 测试以 `sudo -n` 运行。系统 Python 3.10 不支持现有脚本的 `hashlib.file_digest`，所以升权后仍使用上述绝对 Python 路径。

`remote-base` 是迁入 Round7 基线重新编译的版本，`remote-handoff` 保留交接时 residual 实验，`remote-nres` 撤下 residual，`remote-quad` 增加四行 AVX2，`remote-flat` 是已拒绝的连续结果分配，`remote-final` 是最终源码快照。它们的源码、二进制 SHA256 在 `results/remote-*-source.json`、`remote-*-binary.json` 中。

## 编译、格式和正确性

```bash
"$LAB_PY" "$LAB_DIR/remote.py" build replay-base --source "$MVDB_LAB_ROOT/remote-base"
"$LAB_PY" "$LAB_DIR/remote.py" build replay-final --source "$MVDB_LAB_ROOT/remote-final"
"$LAB_PY" "$LAB_DIR/remote.py" run replay-fmt -- make -C minivectordb fmt "GOLANGCI_LINT=$LAB_LINT"
"$LAB_PY" "$LAB_DIR/remote.py" run replay-lint -- make -C minivectordb lint "GOLANGCI_LINT=$LAB_LINT"
"$LAB_PY" "$LAB_DIR/remote.py" run replay-tests -- "$MVDB_LAB_GO" -C minivectordb test ./... -count=1
"$LAB_PY" "$LAB_DIR/remote.py" run replay-oracle -- "$MVDB_LAB_GO" -C minivectordb test ./internal/engine -run 'Test(QuadAVX2|Fused|Prefilter|Fold)' -count=1 -v
"$LAB_PY" "$LAB_DIR/remote.py" run replay-fallback-tests -- env MVDB_NO_SIMD=1 "$MVDB_LAB_GO" -C minivectordb test ./internal/engine -count=1
"$LAB_PY" "$LAB_DIR/remote.py" run replay-race -- env CGO_ENABLED=1 "$MVDB_LAB_GO" -C minivectordb test -race ./internal/engine ./internal/proto ./internal/sqlparse -run 'Test(QuadAVX2|FastInsert|Insert|Partial|Typed|Scan|Filtered|Fused|Request|Parse|Unescape)' -count=1
"$LAB_PY" "$LAB_DIR/remote.py" run replay-edges -- python3 performance/exact-tbm/exact_edges.py --binary remote-final --bin-dir "$MVDB_LAB_ROOT/bin" --assert-correct
"$LAB_PY" "$LAB_DIR/remote.py" run replay-protocol -- python3 protocol_check/run.py --command "[\"$MVDB_LAB_ROOT/bin/remote-final\",\"--data-dir\",\"{data_dir}\"]" --output "$LAB_DIR/results/replay-protocol-public"
bash "$LAB_DIR/remote_crash.sh" replay
```

crash 脚本对最终二进制运行三个独立种子，每种子 40 轮随机 kill/restart。完整 capacity 流程还有下文明确的四次 SIGKILL。

## 五轮原生 T/B/M

```bash
"$LAB_PY" "$LAB_DIR/remote.py" run replay-driver-build -- "$MVDB_LAB_GO" build -trimpath -o "$MVDB_LAB_ROOT/bin/tbm" "$LAB_DIR/remote_tbm_affinity.go"
"$LAB_PY" "$LAB_DIR/remote.py" run replay-audit-build -- "$MVDB_LAB_GO" build -trimpath -o "$MVDB_LAB_ROOT/bin/remote-tbm-audit" "$LAB_DIR/remote_tbm_audit.go"
bash "$LAB_DIR/remote_matrix.sh" replay
```

矩阵明确使用保留的 `remote-base`、`remote-final` 二进制；如需使用上面重编译的 `replay-base`、`replay-final`，先核对各自 SHA256 相等，或复制脚本并替换二进制名。矩阵含默认两个种子、5,000 次查询、10 万行/256 维、127/1024 维、常数、离群、多行转义、4 MiB 部分索引和强制 SIMD 回退。脚本末尾单列 127 维第二个种子 5,000 查询复验。

稳定的 AVX2 同二进制开关比较：

```bash
sudo -n env MVDB_LAB_ROOT="$MVDB_LAB_ROOT" MVDB_LAB_GO="$MVDB_LAB_GO" \
  taskset --cpu-list 18 "$LAB_PY" "$LAB_DIR/remote.py" bench replay-avx2-isolated \
  --bins remote-quad:MVDB_NO_QUAD_AVX2=1,remote-quad --queries 20000 -seed 34347 -cpu-affinity 16-17
```

数据库限定 CPU 16–17，驱动 CPU 18，cgroup CPU 配额仍为 2 CPU、内存 2 GiB、swap 0、fdatasync。每组为五轮 AB/BA，oracle 生成与核对在计量之外。普通比较坚持跨版本响应哈希完全相同；SIMD/标量回退用 `audit_fallback.py` 单独核对每个主键、顺序和距离误差，不为字节不同的回退比较计算 E。

微基准仅用于解释内核，不代替上述端到端比较：

```bash
"$MVDB_LAB_GO" -C minivectordb test -c -o "$MVDB_LAB_ROOT/bin/replay-engine.test" ./internal/engine
sudo -n systemd-run --scope -p CPUQuota=200% -p MemoryMax=2G -p MemorySwapMax=0 \
  env GOMAXPROCS=2 GOGC=20 "$MVDB_LAB_ROOT/bin/replay-engine.test" \
  -test.run '^$' -test.bench BenchmarkQuadAVX2 -test.benchtime 300ms -test.count 3
```

## 超 2 GiB 流式、持久化和大数据比较

```bash
sudo -n env MVDB_LAB_ROOT="$MVDB_LAB_ROOT" MVDB_LAB_GO="$MVDB_LAB_GO" \
  "$LAB_PY" "$LAB_DIR/remote_stress.py" remote-final --stress-label replay-stress
python3 "$LAB_DIR/make_remote_large.py"
"$(dirname "$MVDB_LAB_GO")/gofmt" -w "$LAB_DIR/remote_large/main.go"
"$LAB_PY" "$LAB_DIR/remote.py" run replay-large-build -- "$MVDB_LAB_GO" build -trimpath -o "$MVDB_LAB_ROOT/bin/remote-large" "$LAB_DIR/remote_large/main.go"
sudo -n env MVDB_LAB_ROOT="$MVDB_LAB_ROOT" MVDB_LAB_GO="$MVDB_LAB_GO" \
  taskset --cpu-list 18 "$LAB_PY" "$LAB_DIR/remote_large_run.py" replay-large remote-final \
  --baseline remote-base --fixture "$MVDB_LAB_ROOT/replay-stress/db/data.log" --queries 6
```

本次实际 capacity 标签为 `remote-final-stress-v2`；首次 `remote-final-stress` 因包装器 Path JSON 序列化失败，尚未启动数据库，失败文件没有覆盖。

capacity 每次必须使用新目录；550,000×1024 float32 = 2,252,800,000 字节。查询 oracle 流式独立生成，事务/恢复/全量核对见原始 JSONL。大数据比较仅对私有文件副本执行 `fdatasync`/`POSIX_FADV_DONTNEED` 和 `mincore` 冷页检查；不要执行整机 `drop_caches`。Linux 6.5 的 memory.peak 无法重置，capacity 报整段流程峰值，大数据比较每试次新 cgroup，只对 `service_total` 报可比较 M/E。冷查询与同进程重复查询只分别比较 T/B，不冒充独立阶段内存峰值。

## 实际 Docker 链路

```bash
"$LAB_PY" "$LAB_DIR/remote_docker.py" --prefix replay-docker
"$LAB_PY" "$LAB_DIR/remote.py" run replay-docker-protocol -- python3 protocol_check/run.py \
  --image seekdb-remote-20261117:remote-final --output "$LAB_DIR/results/replay-docker-public"
sudo -n env MVDB_LAB_ROOT="$MVDB_LAB_ROOT" MVDB_LAB_GO="$MVDB_LAB_GO" \
  taskset --cpu-list 18 "$LAB_PY" "$LAB_DIR/remote.py" bench replay-docker \
  --bins remote-base,remote-final --queries 1000 -seed 20261119 -cpu-affinity 16-17 \
  -transport docker -image-prefix seekdb-remote-20261117:
```

Docker helper 使用根目录原始 Dockerfile，从源码构建两份镜像，核验快照哈希，保存镜像 ID、提取二进制 SHA256 和 `go version -m`。每次比较的容器/父 slice 包含完整生命周期 B/M；Docker 结果单列，不混入原生中位数。重建 Docker 标签只影响本实验镜像；禁止删除其他用户镜像、容器或数据。

最终证据入口为 `REMOTE_RESULTS.json`、`REMOTE_REPORT.md`、`REMOTE_IMPLEMENTATION.patch`。失败记录、负收益结果和历史 Intel/WSL 数据全部保留；没有向正式平台提测或推送仓库。
