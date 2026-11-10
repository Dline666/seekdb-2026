-- 向量列记录布局回归：不同行只在最后一个分量上有差异，
-- 若距离计算漏掉 u32 维度前缀或错位，排序结果会明显不同。
CREATE TABLE v (id BIGINT PRIMARY KEY, embedding VECTOR(4));
INSERT INTO v VALUES
    (1, '[0,0,0,100]'),
    (2, '[1,0,0,0]'),
    (3, '[0,1,0,0]'),
    (4, '[0,0,0,0]');

SELECT id, L2_DISTANCE(embedding, '[0,0,0,0]') AS d FROM v ORDER BY d;
SELECT id FROM v ORDER BY L2_DISTANCE(embedding, '[0,0,0,0]') DESC LIMIT 2;
