-- 语句切分冒烟：注释、字符串与向量字面量中的分号都不应切断语句
# 井号注释同样跳过
CREATE TABLE docs (
    id BIGINT PRIMARY KEY,
    category VARCHAR(32),
    content VARCHAR(1024),
    embedding VECTOR(4)
);

INSERT INTO docs VALUES
    (1, 'a;b', '字符串里的分号;不切分', '[1, 0, 0, 0]'),
    (2, 'database', 'database',      '[0, 1, 0, 0]');

/* 块注释里的 ; 也不切分 */
SELECT id, content FROM docs WHERE category = 'a;b';

BEGIN;
UPDATE docs SET embedding = '[0.8, 0.2, 0, 0]' WHERE id = 1;
ROLLBACK;

SELECT id FROM docs ORDER BY id;
