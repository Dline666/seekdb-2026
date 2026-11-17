"""Render the Chinese technical report from validated, immutable results."""
import argparse
import json
from pathlib import Path
from statistics import median

HERE = Path(__file__).resolve().parent


def change(a,b):
    return 100*(b/a-1)


def pair(a,b,digits=4):
    return f'{a:.{digits}f} → {b:.{digits}f}'


def main():
    ap=argparse.ArgumentParser()
    ap.add_argument('--output',type=Path,default=HERE/'REPORT.md')
    args=ap.parse_args()
    data=json.loads((HERE/'RESULTS.json').read_text())
    assert data['status']=='completed'
    profile=json.loads((HERE/'results/profile-summary.json').read_text())
    comparisons=data['final_comparisons']
    large=data['large_query_service']
    a,b=large['versions']
    la,lb=a['scopes']['service_total'],b['scopes']['service_total']
    lm0,lm1=la['median'],lb['median']
    scores=[]
    for x,y in zip(la['runs'],lb['runs']):
        scores.append(sum(w*(x[k]+eps)/(x[k]+y[k]+2*eps)
                          for k,w,eps in [('t_s',.6,.001),('b_bytes',.2,1048576),('m_bytes',.2,1048576)]))
    cap=data['capacity']
    checked=sum(sum(v['checked_queries'] for v in c['versions']) for c in comparisons.values())
    text=f'''# MiniVectorDB 深入 T/B/M 优化报告

日期：2026-11-17。服务器：AMD Ryzen Threadripper PRO 5975WX，Linux 6.5，AVX2/FMA，无 AVX-VNNI/AVX512。实验与开发均在本机；Go 固定 1.22.2，golangci-lint 2.15.0。未正式提测、未推送 GitHub，未修改用户对话。

当前用户启动进程的参数核验为 Codex `--yolo`、`gpt-6-astra`、`model_reasoning_effort="max"`，证据在 `results/session-mode.json`。本轮自动研究和实现单列为实验记录，不作为用户学习对话证据。

## 结论与交付

选择 `advanced-final`：**有界 parser token 复用、单遍类别候选构建、候选块保守上界缓存、AVX2 量化编码**。生产源码已在核对上轮快照后合入工作树，仅涉及本轮列出的 14 个文件（含测试），没有重置已有未提交工作。二进制 SHA256：`{data['candidate_sha256']}`；实际 Docker 构建提取的二进制完全相同。

最明确的收益来自超内存访问结构：55 万行、1024 维、原始向量超过 2 GiB，五轮完整查询服务 T **{pair(lm0['t_s'],lm1['t_s'],3)} s（{change(lm0['t_s'],lm1['t_s']):+.1f}%）**，物理 B **{pair(lm0['b_bytes']/1e9,lm1['b_bytes']/1e9,3)} GB（{change(lm0['b_bytes'],lm1['b_bytes']):+.1f}%）**，M 接近相同的 2 GiB。其同轮配对查询服务 E 中位数为 **{median(scores):.6f}**，基线 0.5；这不是含写入阶段的完整 E。

内存内 10 万行的主要收益是更快的准备，实际 Docker 10 万行完整 E 五轮均改善。默认短查询有改善也有明显噪声；默认维度长序列原生收益很小，1024 维纯查询有退步。最终没有保留初始化日志块、增大 GOGC/索引预算、查询 scratch 或批量泛型构建方案。选择依据是完整 T/B/M 与正确性，而不是只取最快的某一阶段。

主基线是上一轮 `remote-final`，SHA256 `{data['baseline_sha256']}`。原 Round7 `remote-base` 另列实测对照。所有数字均为**本机相对 E**；未知官方固定基线，不预测正式分数或名次。

## 测量与统计口径

固定 cgroup `cpu.max=200000 100000`、`memory.max=2147483648`、`memory.swap.max=0`；数据库 CPU 16–17、外部驱动 CPU 18；GOMAXPROCS=2、GOGC=20、GOMEMLIMIT=1288490188、索引预算 256 MiB、fdatasync。4 MiB 部分索引是明确单列的压力消融。物理 I/O 按校准后的 259:0 设备计量，包含读和写。测试串行使用实验锁，没有整机 drop_caches；超内存仅驱逐私有 inode，并用 mincore 核验冷页。

`E_phase=0.6*f(R_T)+0.2*f(R_B)+0.2*f(R_M)`，`E=(E_write+2*E_query)/3`，`f(R)=R/(1+R)`；epsilon 为 1 ms、1 MiB、1 MiB。最终使用每轮相邻 A/B 的实际基线作配对，基线每轮 E=0.5；五轮顺序 AB/BA/AB/BA/AB。原始前三轮中位数锚点分析也完整保留，没有重写旧结果。

主 T：写入含启动、DDL、装载、退出及最终文件同步；查询含启动、索引构建/预热、查询、退出及最终同步。查询进程为隔离测量而重复进行的 fixture 装载已在写入阶段计费，另提供将这次装载也加入查询 T 的 **whole-process E**。B/M 在两种口径下都覆盖完整装载、准备、预热和最终写回。原始纯阶段 `t_s` 同样保留。没有把预计算转移到计量之外。

每组共享独立 scalar float32 真值，真值生成在计量外。{len(comparisons)} 组最终矩阵累计核验 **{checked:,}** 次查询，所有评分比较的响应哈希一致。最终驱动记录 `writeback_s`；早期探索版本缺这项 T 分解，已标记，不将其绝对 E 与最终值混算。profile/perf/strace 单独执行，绝不作为计分样本。最后一组 100k 长序列用于核验最终诊断中 CPU 增长的疑点，全部五轮保留。

## 最终六项 T/B/M

下表每格为五轮中位数「上轮 → 本轮」；T 使用上述包含准备的口径，B/M 单位 MiB，完整整数与全部逐轮样本在 [RESULTS.json](RESULTS.json)。各负载没有官方给定的混合权重，因此不合成为一个人为总分。

| 负载 | 写 T (s) | 查 T (s) | 写 B | 查 B | 写 M | 查 M | 配对 E |
|---|---:|---:|---:|---:|---:|---:|---:|
'''
    labels={
        'default-1':'默认种子 41117','default-2':'默认种子 41119',
        'long':'默认维度，20k 查询','100k':'100k 行 × 256',
        'dim127':'127 维，5k 查询','dim1024':'1024 维',
        'selective':'1% 候选，全过滤','dense':'100% 候选，全过滤',
        'constant':'常数','outlier':'离群值','multiline':'多行/转义',
        'partial':'1024 维，4 MiB 索引', 'round7':'相对原 Round7，5k 查询',
        'docker':'Docker 默认','docker-long':'Docker 5k 查询',
        'docker-100k':'Docker 100k × 256', '100k-long':'100k × 256，5k 查询'}
    for name,c in comparisons.items():
        x,y=c['versions']
        cells=[labels[name.removeprefix('final-')],
               pair(x['prepared_T']['write'],y['prepared_T']['write']),
               pair(x['prepared_T']['query'],y['prepared_T']['query'])]
        for k,p in [('b_bytes','write'),('b_bytes','query'),('m_bytes','write'),('m_bytes','query')]:
            cells.append(pair(x[p][k]/1048576,y[p][k]/1048576,3))
        cells.append(f"{c['paired_E']['prepared']['median']:.6f}")
        text+='| '+' | '.join(cells)+' |\n'
    text+='''
## 配对变化、噪声与不利结果

下表 T 变化为五个同轮比值变化的中位数，可能与上表「两个中位数之比」不同；E 的范围和胜出轮数一起给出，不能仅凭中位数宣称统计显著。未删除任何慢轮次。MAD、逐轮 B/M 比值及全部样本在 JSON。

| 负载 | 配对写 T | 配对查 T | 配对 E 范围 | E > 0.5 轮数 | whole-process E |
|---|---:|---:|---:|---:|---:|
'''
    for name,c in comparisons.items():
        s=c['paired_E']['prepared'];p=c['paired_changes_percent']
        text+=f"| {labels[name.removeprefix('final-')]} | {p['write']['prepared_t_s']['median']:+.2f}% | {p['query']['prepared_t_s']['median']:+.2f}% | {s['min']:.6f}–{s['max']:.6f} | {s['rounds_above_baseline']}/5 | {c['paired_E']['whole_process']['median']:.6f} |\n"
    text+='''
不利结果必须和收益一起理解：

- 1024 维纯查询中位数 0.18374→0.19761 s，约退步 7.5%；准备 0.07264→0.04609 s，使主口径总体改善。不能称为所有查询都变快。
- 默认 20k 长序列配对 E 约 0.5005，whole-process 约 0.4998，实际意义接近噪声。两个默认短种子也各有退步轮次。
- 4 MiB 部分索引的两个查询 T 中位数为 0.13678→0.14831 s，约退步 8.4%，而同轮比值中位数方向相反，显示噪声和轮次相关性；whole-process E 下降，不声称该压力配置改善。
- 100k 原生的 1,000 查询组若把重复 fixture 装载也加到查询 T，E 约 0.5000，基本无收益。最终 Docker 100k 两种完整口径均五轮改善，但幅度有限。
- Docker 默认/长序列完整 E 都只有 3/5 轮改善，不能用原生内核收益替代 Docker 结论。

## 超内存五轮与资源

固定独立 fixture，12 条不同 SQL/向量真值（相较探索的 6 条增补验证），每次先恢复，再冷页查询批次，再同进程重放，最后退出。每一轮新建 cgroup，M 只对完整 service_total 比较。冷表示每批开始时行页已驱逐，不表示后续每条查询都无缓存；重放也不声称全部向量常驻。全部类别为 c9，过滤覆盖所有有效行，不冒充 10% 选择率场景。

| 轮 | 基线服务 T | 本轮服务 T | 基线 B (GB) | 本轮 B (GB) | 基线 M (字节) | 本轮 M (字节) | 查询服务 E |
|---|---:|---:|---:|---:|---:|---:|---:|
'''
    for i,(x,y) in enumerate(zip(la['runs'],lb['runs'])):
        text+=f"| {i+1} | {x['t_s']:.6f} | {y['t_s']:.6f} | {x['b_bytes']/1e9:.6f} | {y['b_bytes']/1e9:.6f} | {x['m_bytes']} | {y['m_bytes']} | {scores[i]:.6f} |\n"
    text+=f'''
冷批次中位 T {pair(a['scopes']['cold']['median']['t_s'],b['scopes']['cold']['median']['t_s'],4)} s；重放 {pair(a['scopes']['replay']['median']['t_s'],b['scopes']['replay']['median']['t_s'],4)} s。重放的同轮比值变化中位数为 {100*(median(r['t_s'] for r in large['paired_candidate_over_baseline']['replay'])-1):+.2f}%，不宣称暖重放有收益；完整改善来自冷阶段少扫描。完整服务计入恢复和索引准备，未人为构造写入阶段 E。旧 fixture 内容 SHA 在运行前后核验一致。

另一次新建数据库的容量流程完成 550,000×1024 流式写入、事务回滚/提交、批量更新、四次 SIGKILL（含 ready 前恢复中断）、六个状态共 **{cap['independent_topk_queries']}** 次 TopK 真值、两次共 **{cap['census_rows_checked']:,}** 行全量 census。未提交磁盘尾部独立验证 3 条记录、8,285 字节、CRC 和 2,048 个向量坐标，重启后确实被丢弃。已成功确认的数据在重启后保留。

完整流程 memory.peak 为 **{cap['whole_workflow_peak_bytes']:,} 字节**，比配置上限高 **{cap['peak_above_configured_limit_bytes']:,} 字节**；OOM/oom_kill 均为 0。真实内核峰值原样报告，不裁剪为 2 GiB。Linux 6.5 不能重置 memory.peak，容量各阶段的 M 是累计值，不能相减或拿来假装独立阶段峰值。census 核对 ID、类别与独立生成的范数，未宣称逐行投影比对全部向量；TopK 和事务尾部另有独立向量验证。

## 瓶颈、机制与取舍

默认写入约 0.72 s 中，101 次 fdatasync 内部墙钟约 0.649 s；100k 为 1,001 次、约 6.23 s。初始化日志块可显著缩短这部分等待，但会双倍物理写入。真实 Docker 的存储消融主 E 0.50195→0.48307，所以最终回到原日志格式和同步实现。早期压缩后追加记录在重启丢失的失败版本也保存，绝不进入最终组合。

默认查询的原 profile 中，lexer token 数组约占分配 32%，而 100k 查询主要是 AVX2 筛选和精确回验，GC 占比很小。最终相同诊断查询序列的 query TotalAlloc 为 **{pair(profile['baseline_default']['query']['allocated_bytes']/1e6,profile['final_default']['query']['allocated_bytes']/1e6,3)} MB**，对象数 **{profile['baseline_default']['query']['allocated_objects']:,} → {profile['final_default']['query']['allocated_objects']:,}**；GC 次数 **{profile['baseline_default']['query']['gc_cycles']} → {profile['final_default']['query']['gc_cycles']}**。这只证明分配机制改善，不自动意味着 cgroup M 同比例下降。源字符串引用在成功和错误路径均清除，超 256 token 的缓冲不常驻。

100k 的单次采样诊断中，查询 CPU 5.660→6.177 s 的反向信号也保留。为此补跑独立种子、5,000 查询的五轮非采样对照：主查询 T 2.0071→1.8870 s，配对完整 E 中位数 0.505970，五轮均改善；没有重现该诊断退步。不能将不同采样构建之间的变化强行归因于编码器，也不能用分配下降代替非采样验证。

超内存诊断中，单遍构建将查询段 system CPU 从 7.129 降到 3.655 s，major faults 89,350→46,366，同时分配从 6.394 增到 13.112 MB。这解释了先前 SIMD 微调对大数据没有收益、改变访问次数却有效：字符串列在行日志内，第二遍类别计数/填充会再次触碰大量不驻留的向量页。单遍增长数组用少量堆冗余换掉整轮页访问。

AVX2 新编码仅加速准备，保留相同 float64 运算与半值取整；同二进制开关消融支持收益。缓存使用原有保守上界、严格候选块边界和 mutation 失效，未重新引入 residual bounds。Go 版本、GOGC、GOMEMLIMIT、PGO 均未混入代码收益：GOGC=100 虽稍快但 M 大增，768 MiB 索引暖重放更快但完整服务变慢，均撤下。无指针整数 map 已有 GC 优势；未凭推测重写稀疏主键布局或持久化列存。

Docker 同数据诊断的查询墙钟 1.0166→3.2080 s，数据库 CPU 仅 0.8758→1.1358 s；启动/退出也分别增加约 1.05/0.56 s。额外端到端成本稀释数据库计算收益，未出现 CPU throttling。诊断仅作机制依据，最终结论取上面的五轮实际 Docker 测量。

## 正确性、构建与剩余限制

- 完整 Go 测试、engine/proto/sqlparse race、强制 `MVDB_NO_SIMD=1`、fmt、golangci-lint 2.15.0 均通过。
- AVX2 编码独立 big.Float/有理数 oracle 验证 52,362 个坐标，并测试半值邻域、常数、极值、subnormal、127/1024 维、33025 附近整数上限及 PROT_NONE guard page。标量/向量的编码和 qparam 一致。
- SIMD/标量独立审计 2,000 次查询的排序 ID 一致；最大相对误差 {data['fallback_audit']['max_oracle_relative_error']:.3g}，最大 7 ULP；这些微小距离末位差异不强行说成逐位相同，也不计 E。
- 原生和实际 Docker 各 20 个公开协议用例通过；三个种子各 40 次随机 crash；最小 BIGINT、常数取消误差、排序/过滤/事务边界通过。异常和未提交数据处理保留现有语义。
- 额外 arm64 交叉构建探针失败于原有 VNNI 通用回退符号缺失，基线也失败；本轮不宣称 arm64 支持。目标 Linux amd64 回退已实测。
- SIGKILL 是进程崩溃验证，不是物理断电设备试验；完整容量没有逐请求 deadline，不宣称通过正式时限。五轮范围是本次噪声描述，不是跨机器统计保证。

## 可审查证据

[RESEARCH.md](RESEARCH.md) 记录瓶颈、公式、外部资料 URL/日期/适用版本及本机验证；[EXPERIMENTS.md](EXPERIMENTS.md) 记录消融、失败与撤销；[RESULTS.json](RESULTS.json) 保存六项原始样本、配对变化、噪声与三种 E；[REPRODUCE.md](REPRODUCE.md) 与 `results/commands.jsonl` 保存命令。编译器逃逸/BCE 输出、profile、perf、syscall、完整测试日志均在本目录。

最终补丁为 [sources/FINAL_IMPLEMENTATION.patch](sources/FINAL_IMPLEMENTATION.patch)，所有候选源码（包括错误版本）有归档和逐文件哈希。生产快照及二进制继续保留在 `experiment-runtime/advanced-final` 和 `bin/advanced-final`。旧目录、旧根 RESULT.json、agent/conversation.json 的保护哈希全部一致。根 `ADVANCED_TBM_STATUS.json` 记录本轮完成；本次为工作树交付，没有创建提交或对外发送消息。
'''
    with args.output.open('x') as f:
        f.write(text)
    print(args.output)


if __name__=='__main__':
    main()
