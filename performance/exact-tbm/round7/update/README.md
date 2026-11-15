ROUND7 已建索引更新对照。此驱动独立复制大数据驱动的计量工具，不修改旧驱动，
专门测量首次 TopK 已构建隐式索引以后，UPDATE/COMMIT 的确认成本及后续查询。
普通 tbm 的写入阶段位于首次 TopK 之前，不能代替本项证据。

默认每次 trial 全新生成 10,000×1024 float32，原始向量 40,960,000B；2 CPU、
2GiB、无 swap、4MiB 派生索引预算、fdatasync、exact。完整 int8 codes 本身为
10,240,000B，超过 4MiB，基础 slots 估计为 640,000B。该配置用于迫使完整量化
表示无法容纳；实际部分覆盖长度仍需引擎机制诊断确认。本实验不是超过 2GiB
数据的容量检查，也不替代最终全新写入、修改、事务和崩溃恢复 stress。

两版运行五轮 AB、BA、AB、BA、AB；每版每轮使用全新的独立目录和数据库。
全部进程复用同一 cgroup。每次 trial 的阶段为：

1. startup；流式 batch64 load，SQL/向量生成排除 T，CREATE TABLE 及每批确认计入 T。
2. index_query，六条独立 TopK；第一条实际构建隐式索引，过滤查询还建立派生集合。
3. category_update：`UPDATE docs SET category='c8'`，所有行初始为 c9，必须真实变化。
4. point_update_front：id1 的向量替换为 seed=`rows+100001`。
5. point_update_tail：最后一行向量替换为 seed=`rows+100002`。
6. updated_query 六条 TopK；updated_census 检查全部主键、c8 分类及独立范数。
7. shutdown，必须成功退出；读取已提交日志完整 SHA256，Sync/FADV_DONTNEED 并用 mincore 验证冷页。
8. recovery 从 exec 前计时；recovered_query 六条 TopK；recovered_census 全表核验。
9. final_shutdown；确认日志长度及 SHA256 不变，再显式同步并驱逐该 trial 自己的文件，验证至多一个残页，然后进入下一 trial。

更新确认时间包含 fdatasync 及同步的索引维护；三个更新阶段都发生在 index_query
完成之后，响应 affected_rows 必须为 rows、1、1。全表类别修改覆盖量化前缀和
精算尾部，两个向量更新又覆盖首尾；不通过回滚来冒充提交更新。初次装载和建
索引的成本保留，更新后和重启后重新建立派生集合的成本也保留。

行向量使用公开的行局部 xorshift signed-uint8/16 生成器，初始 seed=id。
独立 oracle 在所有被测进程启动前，针对初始状态和完成全部更新的状态各作一次
流式标量 float64 全表穷举，按平方距离、主键维护 K=10，并独立保存每行范数。
不导入生产代码，不保存完整向量表，不以基线返回作为真值。六个查询向量为零、
两个随机向量、原始 id17、预定的新首行向量、预定的新尾行向量，交错无过滤及
当前全表分类过滤。SQL、seed 定义、期望 TopK 及哈希归档；不归档大体积响应。
每条实际响应均校验请求 ID、结果数量、主键、类别、有限距离及并列排序；期望
零距离要求严格为零，其他距离容差为 `1e-12*max(1, expected)`。

默认每 trial 校验 18 次 TopK 响应和两次 10,000 行 census；每版五轮为 90 次
TopK、10 次 census、100,000 行 census，两版总计 180 次 TopK、200,000 行 census。
唯一的“状态＋SQL”TopK 真值为 12，重启后的重复验证不算新的唯一真值。
census 只验证主键、分类、独立标量范数，不声称逐元素核验全部向量。

T 包含协议往返和输出，SQL 生成、外部 oracle、响应解码/核验、文件哈希与显式
驱逐排除 T。各阶段 B/M 在外部验证前冻结，load 只在 T 中排除生成，B/M 覆盖
整个装载阶段。B 是数据设备 cgroup 物理 I/O 增量；M 使用同一打开 FD 重置并
读取 memory.peak。anon/file/RSS 每 50ms 采样，不能把各自最大值相加。缺少
峰值重置、OOM、错序、缺轮、错误计数、非零退出或真值差异都会拒绝结果。

初次查询保留刚装载的本进程页缓存；更新阶段沿用同一进程。重启前仅驱逐本
trial 的日志，并确认冷页；每次结束后也驱逐该文件，防止前一 trial 的缓存
计费累积到下一 trial。没有系统级 drop_caches，不触碰其他 fixture。

构建及运行由主代理串行调度，示例：

```sh
go build -trimpath -o /tmp/seekdb-round7/bin/update performance/exact-tbm/round7/update/main.go
/tmp/seekdb-round7/bin/update \
  -bins /tmp/seekdb-round7/bin/base,/tmp/seekdb-round7/bin/partial \
  -work-dir /tmp/seekdb-round7/indexed-update-private \
  -rows 10000 -dim 1024 -index-mem-mb 4 -batch 64
python3 performance/exact-tbm/round7/update/analyze.py /tmp/seekdb-round7/results/indexed-update.jsonl
```

调用者独占创建 JSONL、stderr 及包含 argv/exit_code 的同名 `-run.json`；分析器
也支持 `--run`。它严格消费全部原始阶段及 AB/BA position，核验进程、请求序列、
两个独立状态真值、更新行数、全表 census、日志哈希及每次退出后的私有页驱逐。
所有五轮均保留。输出每阶段 T/B/M 和配对比值，另列三个更新 ACK 的 T/B 之和、
M 最大值，以及全流程已测阶段的相同合计；**不计算带权 E 或正式总分**。
零分母比值为 null，原始指标继续保留。默认数据量低于自动压缩阈值；若更改
规模使重启触发日志压缩，本驱动的日志不变约束会拒绝该运行，不能混入当前对照。
没有逐请求超时，本地执行完成不等于通过正式时限。
