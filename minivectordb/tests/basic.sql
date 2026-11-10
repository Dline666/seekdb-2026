-- 基础语义回归：排序、并列主键规则、过滤、LIMIT、类型与错误
CREATE TABLE t (id BIGINT PRIMARY KEY, name VARCHAR(8), v VECTOR(3));
INSERT INTO t VALUES (5, 'b', '[0,0,1]'), (2, 'a', '[0,0,1]'), (9, 'a', '[9,9,9]');

-- 并列按主键升序
SELECT id, name FROM t ORDER BY name;

-- 多列排序
SELECT id FROM t ORDER BY name DESC, id DESC;

-- 比较运算与 AND
SELECT id FROM t WHERE id <= 5 AND name = 'a';
SELECT id FROM t WHERE 2 < id AND id < 9;

-- LIMIT 0 与空结果
SELECT id FROM t LIMIT 0;
SELECT id FROM t WHERE id = 999;

-- 距离排序，并列按主键升序
SELECT id, L2_DISTANCE(v, '[0,0,0]') AS d FROM t ORDER BY d LIMIT 2;

-- 带列名的插入
INSERT INTO t (id, name, v) VALUES (1, 'z', '[1,1,1]');
SELECT id FROM t WHERE id = 1;

-- 删除后可以重新插入同一主键
DELETE FROM t WHERE id = 1;
INSERT INTO t VALUES (1, 'z', '[1,1,1]');

-- 错误：重复主键、表定义非法、类型不匹配
INSERT INTO t VALUES (1, 'dup', '[1,1,1]');
CREATE TABLE bad (id BIGINT, name VARCHAR(4));
CREATE TABLE bad2 (id VARCHAR(4) PRIMARY KEY);
INSERT INTO t VALUES (7, 'x', '[1,2]');
UPDATE t SET id = 10 WHERE id = 2;
SELECT COUNT(*) FROM t;
SELECT * FROM t WHERE id > 1 OR id < 3;

-- 向量索引与索引维护
CREATE VECTOR INDEX idx_v ON t(v);
UPDATE t SET v = '[2,2,2]' WHERE id = 5;
DELETE FROM t WHERE id = 9;
INSERT INTO t VALUES (9, 'n', '[1,2,2]');
SELECT id, L2_DISTANCE(v, '[0,0,0]') AS d FROM t ORDER BY d;

-- 事务：回滚恢复删除
BEGIN;
DELETE FROM t;
SELECT id FROM t;
ROLLBACK;
SELECT id FROM t ORDER BY id;
