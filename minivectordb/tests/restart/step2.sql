SELECT id, name, v FROM docs ORDER BY id;
SELECT * FROM other;
CREATE VECTOR INDEX idx2 ON docs(v);
