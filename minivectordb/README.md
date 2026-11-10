# MiniVectorDB

OceanBase 2026 加赛「轻量向量数据库」的本地实现工程。已实现官方 stdin/stdout JSONL 协议，公开用例 20/20 通过。

## 通信协议（官方 STDIO_PROTOCOL.md）

`docker run -i` 启动后，程序先输出并 flush：

```json
{"type":"ready"}
```

之后逐行接收 JSON 请求、逐行返回 JSON 响应：

| 请求 | 响应 |
|---|---|
| `{"id":1,"op":"execute","sql":"...","search_mode":"exact"}` 查询 | `{"id":1,"ok":true,"columns":[...],"rows":[[...]]}` |
| 同上，INSERT/UPDATE/DELETE | `{"id":1,"ok":true,"affected_rows":N}` |
| 同上，DDL/事务控制 | `{"id":1,"ok":true}` |
| 执行失败 | `{"id":1,"ok":false,"error":{"code":"...","message":"..."}}` |
| `{"id":4,"op":"shutdown"}` | `{"id":4,"ok":true}` 后回滚未提交事务、落盘、退出码 0 |

- 原样回传 `id`（保持 JSON 类型）；每行响应后立即 flush；stdout 只有协议报文，日志全走 stderr。
- BIGINT 输出 JSON 整数；向量输出 JSON 数组（float32 最短表示）；距离为有限 JSON 数字。
- `search_mode` 为 `ann` 时仍返回精确结果（Recall 为 1.0，满足召回要求）。

本地调试可用 `--protocol table`（`ROWS n` / `OK n` 文本）或 `--protocol echo`；**默认是 `json`**。

## SQL 子集

- `CREATE TABLE`：`BIGINT` / `VARCHAR(n)` / `VECTOR(n)`，一个 BIGINT 主键，最多一个向量列。
- `INSERT`（多行、可选列名列表）、`SELECT`（投影、`*`、`AS` 别名、比较、`AND`、`ORDER BY`、`LIMIT`、`L2_DISTANCE`）、`UPDATE`、`DELETE`。
- `BEGIN` / `COMMIT` / `ROLLBACK`；`CREATE VECTOR INDEX`（记录元数据，检索仍为精确扫描）。
- 不支持 NULL、JOIN、聚合、子查询、`ALTER TABLE`、外键、嵌套事务、主键更新，均返回错误。

## 语义约定

- `VARCHAR` 长度按 Unicode 码点计数，比较按码点顺序。
- `VECTOR` 存 float32，拒绝 NaN/Inf、维度不符、float32 溢出。
- 过滤后的 TopK 在满足 `WHERE` 的集合内计算；距离并列、普通排序并列都按主键升序。
- 修改类语句返回精确影响行数；语句失败不留下部分修改。

## 存储设计

- 行存 8 KiB 槽页，每表一个 `<表名>.heap`；RID =（页号，槽号），墓碑删除与页内压缩。
- 固定帧数缓冲池（`--buffer-pages`，默认 2048 帧 = 16 MiB），LRU 淘汰与脏页回写。
- 主键索引为内存哈希，启动时扫描堆重建。
- **WAL 先于页面应用**：修改先产出变更记录，写 WAL 并 `fdatasync` 后才改页；不需要 undo，也不靠钉住页面防提前落盘。
- 事务写集合在内存覆盖层中，查询合并覆盖层实现读己之写；`COMMIT` 时才写 WAL 并应用。
- 启动：打开堆文件、重建索引、重放已提交 WAL、回写脏页、写 `catalog.meta`、截断 WAL。

数据目录：`catalog.meta`（表定义）、`<表名>.heap`（行数据）、`wal.log`（预写日志）。

## 性能

- 距离计算 AVX2（4 路 double 累加，运行时检测，回退标量）。
- 语句读入直接读文件描述符（`fread` 会等读满缓冲，交互式协议下会卡死）。
- 记录编码批量写 + 容量预留；TopK 有界堆且只为入选候选分配排序键。

本地测量（10k×128、100 批提交、1000 次 TopK、K=10、一半过滤）：

- 容器：加载 1.84 s、查询 2.93 s；WSL ext4：整流程约 2.0–2.3 s；峰值内存约 20 MB。
- 超内存：10 万行 × 128 维（54 MB 堆）在 512 KiB 缓冲池下加载峰值 8.1 MB、查询 7.2 MB，TopK 与 Python 独立复算完全一致。

## 目录

```
CMakeLists.txt
Dockerfile            多阶段构建，提交时放在仓库根目录
src/sql/              词法分析、AST、递归下降解析
src/exec/             表目录、执行器、WAL 记录应用
src/storage/          页、文件管理、缓冲池、堆表、记录编解码、WAL、catalog
src/protocol/         JSONL 会话与 JSON 编解码、文本调试格式、语句切分
src/common/           值类型、schema、错误、执行结果
tests/public/         官方公开题包副本（20 用例 + protocol_check）
tools/                构建、镜像、回归、崩溃测试、公开用例校验
```

## 构建与运行

```bash
bash tools/build.sh --release
./build/release/minivectordb --data-dir ./tmp/data            # JSONL 协议
printf '%s\n' '{"id":1,"op":"shutdown"}' | ./build/release/minivectordb --data-dir ./tmp/data
```

## 测试

```bash
# 官方协议检查器（握手 + 20 组公开用例）
python3 tools/run_public_cases.py --bin ./build/release/minivectordb
python3 tools/run_public_cases.py --image minivectordb:dev

# 自有回归（文本模式）与崩溃测试
MINIVECTORDB_BIN=./build/release/minivectordb bash tools/smoke.sh
python3 tools/crash_test.py --bin ./build/release/minivectordb --iterations 30 --recovery-rounds 3
```

## 尚未实现

- 近似索引（`ann` 模式返回精确结果，Recall 1.0；未建 ANN 结构）。
- WAL 运行中检查点阈值；堆文件删除只打墓碑，不收缩。
