ROUND7 超过 2GiB 数据的已建索引更新交错对照，独立 fork 自 large 驱动。
用于检验量化复用是否因额外读取旧、新向量而增加物理 I/O；不能用小数据更新
获胜替代此项证据。默认比较 partial-balanced 与 partial-reuse，由 wrapper
指定两版路径。2 CPU、2GiB、无 swap、256MiB 派生预算、fdatasync、exact。

输入是现存完成后的 final-all-c9 日志：550,000×1024，三条删除的 ID 分别为
rows/3、rows/2、rows-1，IDs 1–16 和 18 使用已替换 seed，其他事务回滚或
被杀未提交操作不存在。默认 live 向量为 2,252,787,712B。现有 ROUND6
`/tmp/seekdb-round6/heap-stress/db/data.log` 或 ROUND3 最终 fixture 均可使用。
源始终只读，完整 SHA256 和 size/mtime 在前后保护。

源只复制一次到新 work-dir 的私有 inode。复制 helper 与数据库使用同一 cgroup，
显式流式 Read/Write 后 Sync，保证新 inode 的写回归属也在该 cgroup；准备阶段
不计入数据库阶段指标，随后仍验证冷页。每版五轮，顺序 AB、BA、AB、BA、AB：

1. 核验私有初始长度/身份，Sync/FADV_DONTNEED/mincore 确认冷页，startup 从 exec 前计时。
2. ready 后确认启动未改变日志，再驱逐私有文件；pre_update 六条 TopK 包含首次隐式索引构建和独立真值校验。
3. 单条 `UPDATE docs SET category='c8'`，必须确认 affected_rows=live_rows；每行 c9→c8 确保真实写入，ACK 包含 fdatasync 和同步索引维护。
4. 同进程、不驱逐缓存，post_update 六条 TopK 使用 c8 真值；随后 census 核验所有 live PK、c8 分类及独立范数。
5. 正常 shutdown，验证响应 ID、退出码与 OOM。
6. 打开私有文件并核验仍为原私有 inode、与源 inode 不同、长度真实增长；只对原始长度的前缀计算 SHA256。前缀不再等于原 fixture 时立即失败，绝不截断。
7. 前缀通过后，同 cgroup 的 reset helper 再核验设备号、inode 和追加后长度，才截断私有尾部回原长度并 Sync；父进程重新核验完整 SHA256/长度/身份，再驱逐该私有文件并确认冷页。下一轮使用同一干净初态。

每个进程均处于同一 cgroup，避免跨轮页缓存计费逃逸。复位不改源，不重写整个
4GiB 日志，不用系统级 drop_caches。日志前缀、完整哈希及显式复位记录均保留。
若启动发生压缩、替换 inode，或前缀任何字节变化，实验拒绝运行或拒绝复位。
更新日志会临时增长约一份完整 live 表，默认约额外 2.25GB；全部文件留在私有目录。

六个固定查询覆盖零、随机向量、原始 id17、替换 id1 和事务替换 id18，交错
无过滤及当前全表分类过滤；过滤仍覆盖全表，不能声称有选择率收益。独立 oracle
按已知生成模型逐行生成，一次标量 float64 全表穷举维护 K=10，并保存独立范数。
更新不修改向量，两个类别状态复用同一份距离真值，分别构造类别正确的期望结果。
不调用生产距离/量化代码，不使用基线响应作为真值，不保存完整向量表。

每条 TopK 校验请求 ID、数量、主键、类别、有限距离、严格零距离及并列主键顺序，
并将响应 hash 绑定到期望 TopK 的规范 JSON 加 LF。census 校验全部 live 行及
独立范数、绑定预先生成的期望完整响应 hash；不声称逐元素比较全部向量。
默认每版校验 60 次 TopK、5 次 census、2,749,985 行 census；两版共 120 次
TopK、10 次 census、5,499,970 行 census。唯一“状态＋SQL”真值为 12，查询
向量只有 6 个，重复响应次数不算新的唯一真值。

保留 startup、pre_update、category_update、post_update、census、shutdown
全部阶段 T/B/M。T 包含协议往返/输出；数据生成、外部 oracle、解码验证、哈希、
私有文件复位及缓存驱逐排除。B 为数据库 cgroup 数据设备物理读写增量，M 使用
同一 FD 重置/读取 memory.peak，anon/file/RSS 每 50ms 采样。各批指标在外部
解码验证前冻结。冷页只保证 pre_update 批开始；其余请求沿用进程和剩余缓存。
`measured_stage_sum` 仅加上述阶段 T/B、取最大 M，无阶段权重、无带权 E、无
正式分数外推。所有负收益轮和配对比值原样保留。

UPDATE 额外记录纯 ACK 时间；阶段 T 还包含 ACK 后的日志长度读取。它校验
日志实际增长至少覆盖全部 live 向量，并要求物理写入不低于该增长减一页。
计费不足立即失败，没有等待计数追赶。创建和复位 helper 自行核验 cgroup 及
资源限制，分析器把 producer、reset 和每个数据库阶段的 cgroup 身份绑定。

初版 same-binary 烟测曾出现真实更新但 cgroup 写入为零：外部进程创建的 inode
导致写回计费归属错误。该版本只保留为诊断记录，不提供更新 B 的性能证据。
v2 改用同 cgroup helper 后，烟测每次更新均记录 258,048B；损坏记录自检另行
验证写入下界、身份、前缀、复位和独立真值约束。

构建/运行由主代理串行调度，示例：

```sh
go build -trimpath -o /tmp/seekdb-round7/bin/large-update performance/exact-tbm/round7/large_update/main.go
/tmp/seekdb-round7/bin/large-update \
  -bins /tmp/seekdb-round7/bin/partial-balanced,/tmp/seekdb-round7/bin/partial-reuse \
  -fixture /tmp/seekdb-round6/heap-stress/db/data.log \
  -work-dir /tmp/seekdb-round7/large-update-private \
  -rows 550000 -dim 1024
python3 performance/exact-tbm/round7/large_update/analyze.py /tmp/seekdb-round7/results/large-update.jsonl
```

root 的 large_update_run.py 独占创建 raw、stderr、run、summary，记录运行前后
driver 二进制和 `large_update/main.go` 两项 SHA256，并确认未变。analyzer
要求完整身份字段、成功退出、原始 AB/BA 顺序/position、所有阶段计量、显式
整数 reference_bad=0、独立响应 hash、UPDATE 行数、前缀校验先于截断、复位
完整 hash 和 mincore 冷页证明。可用 `--run` 指定元数据路径。

功能烟测可使用符合相同最终模型的 1536×33 fixture，显式传 `-allow-small`；
analyzer/`summarize(path,run_path,allow_small)` 也必须允许小数据。这不算大容量
证据。本实验不重启更新后的数据库，最终新写入/更新/崩溃 stress 另证恢复。
没有逐请求超时，本地完成不等于通过正式时限。
