"""Build a report from completed runs; never execute a workload."""
from common import *
import statistics

def read(name):
    return json.loads((OUTPUT/name).read_text())

def delta(b,c):
    return f'{100*(c/b-1):+.2f}%'

def pair(b,c,scale=1):
    return f'{b/scale:.3f} → {c/scale:.3f}'

labels=['docker-memory','docker-memory-multiline','docker-poll','docker-poll-multiline',
        'docker-default','docker-multiline','docker-100k','docker-v2-final','native-default',
        'docker-default-long']
reports={name:read(name+'-summary.json') for name in labels}
for name,s in reports.items():
    assert s['complete_interleaving'] and s['rounds']==5 and s['reference_bad']==0 and s['memory_events_zero']
    assert read(name+'-run.json')['identities_unchanged']
checked=sum(sum(v['checked_queries'] for v in s['versions']) for s in reports.values())
stress_label='candidate-fresh-large-retry'
stress=read(stress_label+'-summary.json')
assert read(stress_label+'-run.json')['validation_passed']
records=[json.loads(line) for line in (OUTPUT/'retention-v2-memory.jsonl').read_text().splitlines()]
retention={}
for name in ['v2','memory']:
    acks=[r for r in records if r.get('candidate')==name and r['phase']=='insert_acknowledged']
    end=next(r for r in records if r.get('candidate')==name and r['phase']=='version_complete')
    recovery=next(r for r in records if r.get('candidate')==name and r['phase']=='recovery_validation')
    retention[name]={'acked_batches':end['batches_acknowledged'],'acked_rows':end['acked_rows'],
        'last_ack_anon_bytes':acks[-1]['telemetry']['memory.stat']['anon'],
        'memory_events':end['telemetry']['memory.events'],
        'full_workflow_passed':end['full_workflow_passed'],'recovery':recovery}
matched_rows=retention['v2']['acked_rows']
for name in retention:
    same=next(r for r in records if r.get('candidate')==name and r['phase']=='insert_acknowledged'
              and r['acked_rows']==matched_rows)
    retention[name]['matched_acked_rows']=matched_rows
    retention[name]['matched_ack_anon_bytes']=same['telemetry']['memory.stat']['anon']
expected=read('candidate-source.json')
actual={rel:sha(REPO/'minivectordb'/rel) for rel in expected}
documentation_changes=[rel for rel in expected if expected[rel]!=actual[rel]]
assert documentation_changes==['README.md'], 'unexpected change after tested build'
save('candidate-final-source.json',actual)
binaries=read('binaries.json')
assert binaries=={n:sha(ROOT/'bin'/n) for n in binaries}
images=read('images.json')
for n,v in images.items():
    current=subprocess.check_output(['docker','image','inspect','--format','{{.Id}}',v['image']],text=True).strip()
    assert current==v['image_id'] and v['binary_sha256']==binaries[n]
summary={'branch':'perf/exact-round2-v3','parent_commit':'f723ad1940d65dc21e3eb53a3541c7fe91e546f0',
    'highscore_baseline_commit':'133e12caf28b9104be9ae329d0c0120f80863dc6',
    'official_submissions':0,'pushed':False,'formal_score_estimate':None,
    'source_unchanged':True,'source_unchanged_scope':'all tested files except README.md documentation',
    'documentation_changes':documentation_changes,'binary_sha256':binaries,'images':images,
    'retention':retention,'comparisons':reports,'topk_response_instances':checked,
    'fresh_large_workflow':stress,'interrupted_attempt':read('candidate-fresh-large-interruption.json')}
save('summary.json',summary)

lines=['**修复 INSERT 请求内存保留，默认关闭 stdin 忙等；本轮完成实际 Docker 对照和全新 >2 GiB 数据完整流程。**','',
    '分支 `perf/exact-round2-v3`，从用户 fork 的 `f723ad1` 派生。性能对照使用用户确认取得约 4200 分的 `133e12c`。'
    '本轮未推送、未正式提测，不预测新的性能分。','',
    '本轮能确认内存缺陷修复和忙等 CPU 消耗降低，但 Docker 查询收益不稳定：'
    '默认短组查询回退约 9.7%，加长组改善约 5.8%，十万行写入基本持平。'
    '因此这是一版消除已知退化的候选，尚无证据证明已经恢复或超过历史正式分数。','',
    '**改动与取舍**','',
    '- `RowParser.ParseRows` 返回前清理保留 token 数组的完整容量，成功和解析失败均释放来源字符串；返回的 AST 值不变。',
    '- INSERT worker 完成或失败后清空 scratch row，避免最后一个字符串值保留整个请求。保留编码、token 和向量 scratch 复用。',
    '- stdin 轮询改为显式设置 `MVDB_SPIN_US` 才启用，默认 0 使用阻塞读取；保留开关并拒绝 duration 溢出值。'
    '此项减少忙等 CPU 成本，不宣称所有查询墙钟都变快。','',
    '**确定的内存退化已修复**','',
    '同一轮 2 CPU / 256 MiB / 无 swap / fdatasync，合法 VARCHAR(60000) 加 VECTOR(1)，每批行数从 160 减至 16。'
    '对照仅改变引用清理：v2 与 memory 均保留旧默认 1000µs 轮询。','',
    '| 版本 | 已 ACK 批数 / 行数 | 最后 ACK 匿名内存 (MiB) | OOM | 完整流程 |',
    '|---|---:|---:|---:|---|']
for n,r in retention.items():
    lines.append(f'| {n} | {r["acked_batches"]} / {r["acked_rows"]} | {r["last_ack_anon_bytes"]/2**20:.3f} | '
                 f'{r["memory_events"]["oom"]} | {"通过" if r["full_workflow_passed"] else "未完成"} |')
lines += ['', f'在相同的 {matched_rows:,} 行 ACK 检查点，匿名内存为 '
    f'{retention["v2"]["matched_ack_anon_bytes"]/2**20:.3f} → '
    f'{retention["memory"]["matched_ack_anon_bytes"]/2**20:.3f} MiB。', '',
    '恢复核验包括全部已 ACK 主键和末 ACK 行内容；没有逐字节比较每一行 payload。'
    '256 MiB 是缺陷诊断，不能称为正式 2 GiB 失败。两项新增回归测试在原生产代码上因引用保留而失败，修复后通过。', '',
    '**性能方法**','',
    'Go 1.22.2 linux/amd64、GOAMD64=v1、GOGC=20、exact、fdatasync。每组五轮交错 AB/BA，'
    '数据库限制 2 CPU / 2 GiB / 无 swap；生成和独立 float64 真值位于限制之外。'
    'Docker 镜像使用与项目相同的锁定 Ubuntu 24.04 基础镜像，复制锁定的本地 Go 二进制，直接以数据库为容器入口。'
    '镜像构建记录与二进制/镜像 SHA 在 `images.json`、`binaries.json`。这是本机实际 Docker 链路，不是平台正式评测。','',
    'Docker 数据使用 Linux ext4 bind mount。容器放入每次新建的持久父 slice，容器退出后读取父级 cgroup 的完整生命周期 '
    'I/O、memory.peak、CPU；校验实际容器 cgroup 属于该父 slice。Docker CLI、daemon 和外部真值不在数据库计量 cgroup 内。'
    '镜像页缓存已由 smoke 预热，每个阶段使用新数据库目录。','',
    'Docker `cpu_throttled_s` 是父 slice 的计数，不能据此排除子容器发生限流；'
    '`cpu_usage_s` 为父级汇总的实际 CPU 用量。正式硬件、挂载方式和计时边界仍可能不同。','',
    '下表 T 为写入 load 或一次预热后的 query 墙钟；B/M 是相应完整进程的 I/O 读写量和峰值内存，含装载和预热。'
    '另存 startup、DDL、load、warmup、query、shutdown 及非重叠计时和 `request_total_s`，不隐藏首次建索引成本。'
    '本地 E 沿用既有方法：T/B/M 权重 0.6/0.2/0.2，写/查权重 1/3、2/3；基准取 A 的前三轮中位数。'
    '这些本地权重不代表隐藏用例权重，仅作本地相对比较，不能换算正式分数。','',
    '| 对照组 | A → B | 写入 T变化 | 查询 T变化 | 写 M变化 | 查 M变化 | 写 B变化 | 查 B变化 |',
    '|---|---|---:|---:|---:|---:|---:|---:|']
for label,s in reports.items():
    b,c=s['versions']
    lines.append(f'| {label} | {b["name"]} → {c["name"]} | {delta(b["write"]["t_s"],c["write"]["t_s"])} | '
        f'{delta(b["query"]["t_s"],c["query"]["t_s"])} | {delta(b["write"]["m_bytes"],c["write"]["m_bytes"])} | '
        f'{delta(b["query"]["m_bytes"],c["query"]["m_bytes"])} | {delta(b["write"]["b_bytes"],c["write"]["b_bytes"])} | '
        f'{delta(b["query"]["b_bytes"],c["query"]["b_bytes"])} |')
lines += ['', '变化率为两个五轮中位数之比减一。原始逐轮配对比值、范围和 local_E 均在各组 summary 中。'
    '`memory` 仅修引用；`candidate` 再关闭默认轮询；`base` 为 4200 基线；`v2` 为用户反馈 3500 的候选。'
    '默认 10k×128/1000 次查询，多行为中文内容和 JSON 转义换行；100k 为 100k×256/200 次查询。'
    '`docker-default-long` 为默认数据、5000 次查询，用于复核短查询组的波动；两组都保留，不择优替换。','',
    '**轮询 CPU 代价**','',
    '| 同一候选：0 → 1000µs | 查询 T (ms) | 查询进程 CPU (ms) | CPU变化 |',
    '|---|---:|---:|---:|']
for name in ['docker-poll','docker-poll-multiline']:
    s=reports[name]; b,c=s['versions']
    x,y=(s['extra_phase_medians'][v['name']]['query']['cpu_usage_s'] for v in [b,c])
    lines.append(f'| {name} | {pair(b["query"]["t_s"],c["query"]["t_s"],.001)} | '
                 f'{pair(x,y,.001)} | {delta(x,y)} |')
lines += ['', 'CPU 为完整查询进程，含装载和预热；不是孤立查询 CPU。忙等的 Docker 墙钟收益明显依赖负载，'
    '同时增加 CPU 消耗。默认关闭是保守的资源选择；现有结果不能证明忙等就是此前掉分 700 的唯一原因。','',
    '**正确性与持久化**','',
    f'- 十组性能对照检查 {checked:,} 条 TopK 响应实例，零真值差异、零 OOM。重复轮次计入此数，不是唯一查询用例数。',
    '- 完整 Go 测试、engine/sqlparse/cmd race 通过；实际 Docker 下 20 项公开协议检查通过。',
    '- 30 轮随机 SIGKILL、exact 边界、普通与分片/强制快速 INSERT 差分均通过；独立 math/big.Rat 数值真值通过。',
    '- 性能真值检查拒绝错误响应或不匹配的 ID/距离；差分测试另比较预期错误响应。'
    '本轮未改变数值算法、exact 剪枝或持久化同步语义。','',
    '**全新大数据完整流程**','',
    f'候选从空目录写入 550,000×1024 个 float32，原始向量字节数 {550000*1024*4:,}，超过 2 GiB。'
    '数据库始终受 2 CPU / 2 GiB / 无 swap 限制；包括写入后查询、更新/删除/回滚、已提交后 kill、'
    '未提交事务落入日志后 kill、恢复 ready 前再次 kill、最终恢复、全表 UPDATE 后再次 kill/恢复及两次完整 census。','',
    f'完整验证通过：独立 TopK {stress["independent_topk_queries"]} 次，'
    f'census 共检查 {stress["census_rows_checked"]:,} 行，SIGKILL {stress["sigkill_events"]} 次，'
    f'未提交尾部正确丢弃 {stress["uncommitted_tail_bytes"]:,} 字节；OOM 计数为 0。',
    '', '这是容量和正确性流程，不是交错性能分数。census 检查全部主键、类别和独立生成的向量范数；'
    '未逐坐标投影所有向量。恢复期间 kill 以“进程存活且未 ready”验证，无内部恢复进度 hook。'
    '没有逐请求正式时限门禁，因此不能宣称通过平台隐藏用例。','',
    f'cgroup memory.max 实际配置为 {stress["memory_limit_bytes"]:,} 字节，整个流程 memory.peak 记录 '
    f'{stress["whole_workflow_peak_bytes"]:,} 字节，比配置值高 {stress["peak_above_configured_limit_bytes"]:,} 字节。'
    '原样保留该计数，不把它截断为 2 GiB；未发生 OOM。峰值包含文件页缓存，不能当作 Go 堆大小。','',
    '| 大数据阶段 | T (s) | B读写 (GiB) | M (MiB) | 采样匿名最大 (MiB) | 采样文件最大 (MiB) |',
    '|---|---:|---:|---:|---:|---:|']
for p in stress['phases']:
    if p['phase'] not in {'write','bulk_update','recovery_after_bulk_commit'} and not p['phase'].startswith('query_after_'):
        continue
    lines.append(f'| {p["phase"]} | {p["t_s"]:.3f} | {p["b_bytes"]/2**30:.3f} | {p["m_bytes"]/2**20:.3f} | '
                 f'{p["max_sampled_anon"]/2**20:.3f} | {p["max_sampled_file"]/2**20:.3f} |')
lines += ['', '匿名/文件采样最大值未必出现在同一时刻，不能相加；T 排除外部独立真值计算。'
    '查询阶段各含 12 次 TopK。文件页缓存占用了大部分受限内存，全表更新后查询的读量明显偏大。'
    '后续应优先验证超过内存容量时的扫描 I/O，以及分块误差上界能否降低精确扫描范围；'
    '本轮没有修改这些算法，也没有大数据交错对照，不能把这张表解释为已经改善这类瓶颈。','',
    '**材料与复现**','',
    '- `summary.json`：所有对照、内存复现和大数据流程汇总。',
    '- `*-run.json`：准确执行参数、退出码、SHA 和耗时；`*.jsonl` 为原始结果。',
    '- `tbm_eval.go` / `benchmark.py`：含 Docker 父 slice 计量和独立真值的交错驱动。',
    '- `stress_driver.go` / `stress_verify.py`：流式 >2 GiB 全流程与严格摘要核验。',
    '- `retention-v2-memory.jsonl`：旧版 OOM / 修复版通过的同轮对照；数据归档见 `retention-archive.json`。',
    '', '新增单测的第一份草稿曾有语法错误，已在 `retention-regression-red-verified` 中修正并确认真正产品失败。'
    '首次 Docker smoke 因 WSL 空闲退出导致 daemon 停止而失败；固定存活后，同二进制五轮 `docker-smoke-v2` 通过。'
    '这些准备阶段失败保留在实验目录，不作为产品性能证据。','',
    '首次大数据流程在 bulk UPDATE 后恢复已 ready、末轮查询前后遭遇 WSL 整体重启，未形成成功摘要；'
    '其零字节 run/summary、原始 JSONL 和数据归档均保留，不能算完整流程通过。'
    '本报告的大数据通过结论只引用 `candidate-fresh-large-retry` 从空库重新执行的结果。','',
    '测试脚本默认路径在 `common.py`；换机器需调整。所有实验文件位于 '
    '`C:\\Users\\27372\\seekdb-fable-v3-experiments`，无数据库 fixture 入库。','']
(OUTPUT/'REPORT.md').write_text('\n'.join(lines))
print(json.dumps({'checked_responses':checked,'source_unchanged':True,'binaries_unchanged':True}))
