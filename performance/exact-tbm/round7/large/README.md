ROUND7 大数据 exact 对照，独立 fork 自 `performance/exact-tbm/sqlcompare`。
不修改旧驱动。它只比较查询服务的 T/B/M，不产生正式分数，也不把本地阶段 E
与写入混合成所谓总分。

源 fixture 必须是已完成 ROUND3 或 ROUND6 stress 的最终 all-c9 数据库：默认
550,000 行、1024 维，IDs 1–16 和 18 已替换，`rows/3`、`rows/2`、`rows-1`
已删除，其余事务回滚或被杀死后未生效，最终全部 live 行 category 为 c9。
优先使用 `/tmp/seekdb-round6/heap-stress/db/data.log`；也可使用
`/tmp/seekdb-round3-stress-final/db/data.log`。不能使用尚未完成 bulk-update 的
completed-basic 日志。默认 549,997 行 live 向量共 2,252,787,712B，超过 2GiB。
此数据的 c9 过滤覆盖全部 live 行，不能用于推断选择率为 10% 的过滤收益。

驱动仅将源日志复制一次到全新 work-dir 的私有 inode；所有轮次在该副本上运行。
源文件及私有副本分别在前后读取完整 SHA256，每次 trial 检查 size/mtime，源与
副本起始哈希必须相同，禁止修改已有 fixture 或每轮重新写入数据库。二进制也在
运行前后验证 SHA256。结果 JSONL 写到 stdout，调用者须独占创建结果和 stderr
文件，并归档包含 `argv`、`exit_code` 的 `<结果 stem>-run.json`；不要覆盖失败轮。

数据库始终使用 2 CPU、2GiB、无 swap、256MiB 派生索引预算、fdatasync、exact、
GOMAXPROCS=2、GOMEMLIMIT=1288490188、GOGC=20。清除继承的 MVDB_* 开关。
一个 cgroup 跨全部进程复用，避免旧页缓存计费逃逸。仅驱逐私有日志，不使用
系统级 drop_caches；Sync/FADV_DONTNEED 后 mincore 必须确认至多一个尾部残页。

每个版本固定运行五轮，顺序为 AB、BA、AB、BA、AB。每次 trial 依次执行：

1. 确认私有日志冷页，启动新进程；startup 从 exec 之前开始计时。
2. ready 后再次驱逐私有日志，测量一个 cold batch。
3. 同进程、不驱逐缓存，立即重复相同 SQL，测量 replay batch。
4. 显式 shutdown，核验响应 ID 和进程退出码。

冷批只保证**批次开始时冷页**；后续请求可能复用页和派生状态。replay 保留进程、
派生状态和残留文件页，不声称超过内存容量的数据已经全部热驻留。首次构建不
藏到未计时预热：服务阶段合计包含 startup、cold、replay、shutdown 的全部 T/B，
M 取四阶段最大值。准备、冷页驱逐、外部 oracle 和结果验证不计入服务阶段。

`-queries 6` 为默认：零向量、两个独立随机向量、原始 id17、替换 id1、已提交
事务替换 id18，交错使用无过滤及全 c9 过滤。`-queries 12` 对同六个向量补齐
另一种过滤方式，得到 12 条唯一 SQL。两种模式均为 K=10。oracle 在所有被测
进程启动前，逐行再生向量，流式执行独立标量 float64 全表穷举并维护前 K；
不调用生产代码，不保存全量向量，不以基线响应作为真值。并列按主键升序。
每条响应验证 request ID、数量、主键、category、有限距离、零距离及有序并列；
距离容差为 `1e-12*max(1, expected)`，期望零距离必须严格为零。原始查询文字、
SQL/真值哈希、期望 TopK、实际请求 ID、每查询时间及标准化响应哈希都会归档。
默认每版验证 60 次响应，共 120 次，唯一 SQL 仍为 6；12 模式相应翻倍。

每批 T 包含 JSONL 请求、协议往返和响应输出。B 为数据设备的 cgroup 物理
读写增量；M 为同一打开 FD 重置并读取的 memory.peak。指标在外部解码/真值
验证前冻结。anon/file/RSS 每 50ms 采样；不同时间最大值不能相加。缺少峰值
重置、冷页不成立、OOM、真值差异、非零退出、文件变化都会使实验失败。

构建和执行必须由主代理串行调度；以下仅为命令示例：

```sh
go build -trimpath -o /tmp/seekdb-round7/bin/large performance/exact-tbm/round7/large/main.go
/tmp/seekdb-round7/bin/large \
  -bins /tmp/seekdb-round7/bin/base,/tmp/seekdb-round7/bin/candidate \
  -fixture /tmp/seekdb-round6/heap-stress/db/data.log \
  -work-dir /tmp/seekdb-round7/large-private \
  -queries 6
python3 performance/exact-tbm/round7/large/analyze.py /tmp/seekdb-round7/results/large.jsonl
```

analyzer 默认读取 `large-run.json`，可用 `--run` 指定。它验证全部原始记录的
顺序、AB/BA position、同进程冷热批、两次冷页、每阶段计量、响应校验、退出、
文件及二进制哈希和最终总数，任何丢失或额外记录都会拒绝。每个阶段保留全部
五轮，包括负收益轮。cold、replay、service_total 的本地 E 使用 T/B/M 权重
0.6/0.2/0.2 和 1ms/1MiB/1MiB 平滑，参考为前面列出的基线前三轮中位数；
不对阶段发明权重，也不转换为平台分数。零分母的原始配对比率输出 null，
原始指标和 E 仍保留。

仅供小数据功能烟测时，可显式传 `-rows`、`-dim`、`-allow-small`，fixture
仍须符合相同最终逻辑模型，analyzer 也须 `--allow-small`。默认驱动和分析器
都拒绝 live 向量不超过 2GiB 的容量声明。此驱动不提供写入/更新收益证据，
选定版仍须另跑全新完整 stress，验证大数据写入、修改、事务与崩溃恢复。
没有逐请求超时，完整执行通过也不等于正式时限通过。
