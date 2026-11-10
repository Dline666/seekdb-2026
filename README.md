# MiniVectorDB（分支根目录运行说明）

本分支 `2026-competition-round2` 的加赛实现。源码位于 `minivectordb/`，根目录 `Dockerfile` 从源码多阶段构建。

## 语言与构建

- C++17，CMake ≥ 3.20，GCC 13（Ubuntu 24.04 自带）。
- 无第三方依赖，只用标准库与 POSIX 接口。

```bash
docker build --platform=linux/amd64 -t minivectordb:latest .
```

## 启动

容器以前台方式运行，数据库从标准输入读 SQL、向标准输出写结果，数据目录为 `/data`：

```bash
docker run --rm -i -v minivectordb-data:/data minivectordb:latest
```

- 入口：`/app/minivectordb --data-dir /data`
- 可选参数：`--buffer-pages N`（缓冲池帧数，默认 2048，每帧 8 KiB）、`--protocol table|echo`
- 就绪、日志与调试信息写标准错误；标准输出只放查询结果与状态。
- `Ctrl-C`/`SIGTERM` 或标准输入关闭时正常退出：回滚未提交事务并完成落盘。

## 通信协议

按官方 `STDIO_PROTOCOL.md` 实现 JSON Lines：

- 启动先输出 `{"type":"ready"}` 并 flush。
- `{"id":N,"op":"execute","sql":"...","search_mode":"exact|ann"}` →
  `{"id":N,"ok":true,"columns":[...],"rows":[[...]]}` 或 `{"id":N,"ok":true,"affected_rows":N}` 或 `{"id":N,"ok":true}`。
- 失败：`{"id":N,"ok":false,"error":{"code":"...","message":"..."}}`。
- `{"id":N,"op":"shutdown"}` → `{"id":N,"ok":true}`，回滚未提交事务、落盘、退出码 0。
- 日志只写 stderr；`ann` 模式返回精确结果（Recall 1.0）。

调试可用 `--protocol table|echo` 输出人类可读文本。

## SQL 支持范围

- `CREATE TABLE`：`BIGINT` / `VARCHAR(n)` / `VECTOR(n)`，一个 BIGINT 主键，最多一个向量列。
- `INSERT`（多行，可选列名列表）、`SELECT`（投影、`*`、`AS` 别名、比较、`AND`、`ORDER BY`、`LIMIT`、`L2_DISTANCE`）、`UPDATE`、`DELETE`。
- `BEGIN` / `COMMIT` / `ROLLBACK`；`CREATE VECTOR INDEX`（记录元数据，检索仍为精确扫描）。
- 不支持 NULL、JOIN、聚合、子查询、`ALTER TABLE`、外键、嵌套事务、主键更新，均返回错误。

## 从空数据目录开始的示例

```
CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(8), content VARCHAR(16), embedding VECTOR(2));
INSERT INTO docs VALUES (1, '甲', '一', '[0, 0]'), (2, '乙', '二', '[3, 4]');
SELECT id, L2_DISTANCE(embedding, '[0, 0]') AS d FROM docs ORDER BY d LIMIT 2;
BEGIN;
UPDATE docs SET content = '改' WHERE id = 1;
ROLLBACK;
DELETE FROM docs WHERE id = 2;
```

重启：结束进程后以同一 `/data` 重新启动，已提交数据保持一致；未提交事务不会生效。

## 持久化

`/data` 下：`catalog.meta`（表定义）、`<表名>.heap`（8 KiB 页的行数据）、`wal.log`（预写日志，提交前 fsync）。

## 自测

```bash
# 容器内跑公开用例（tests/public 为官方公开样例副本）
python3 minivectordb/tools/run_public_cases.py --image minivectordb:latest
```
