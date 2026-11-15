"""Generate a Chinese ROUND6 draft from completed local evidence only.

Run after selection.json, all-comparisons.json and the selected verification
artifacts exist. This script never builds or runs a database. Missing or
incomplete evidence is an error; it does not fill gaps with estimated results.
"""
import argparse
import json
import math
from pathlib import Path
import re

from run import ROOT, RESULTS


MIB = 1 << 20
BASELINE = '33485b53c039a6cc3d2c4fa5b40d064365cf6eee'


def require(ok, message):
    if not ok:
        raise ValueError(message)


def read_json(path):
    return json.loads(path.read_text(encoding='utf-8-sig'))


def json_lines(path):
    return [json.loads(line) for line in path.read_text(encoding='utf-8-sig').splitlines()
            if line.strip()]


def finite(value, label):
    require(isinstance(value, (int, float)) and not isinstance(value, bool)
            and math.isfinite(value) and value >= 0, f'invalid {label}: {value!r}')
    return value


def cell(value):
    return str(value).replace('|', '\\|').replace('\n', ' ')


def table(headers, rows):
    require(rows, f'empty table: {headers}')
    return '\n'.join(['| ' + ' | '.join(headers) + ' |',
                      '| ' + ' | '.join('---' for _ in headers) + ' |'] +
                     ['| ' + ' | '.join(cell(x) for x in row) + ' |' for row in rows])


def evidence(name, label=None):
    return f'[{label or name}](results/round6/{name})'


def base_name(name):
    return name.split(':', 1)[0]


def workload(report):
    w = report['workload']
    text = f"{w['rows']:,}×{w['dim']} / {w['queries']:,} 查询"
    if w['data'] != 'uniform':
        text += ' ' + w['data']
    if w['seed'] != 7:
        text += f" seed={w['seed']}"
    if any('MVDB_NO_AVXVNNI=1' in v['name'] for v in report['versions']):
        text += ' 禁用 AVX-VNNI'
    return text


def arrow(left, right, digits=6, divisor=1):
    return f'{left/divisor:.{digits}f}→{right/divisor:.{digits}f}'


def percent(left, right):
    require(left > 0, 'percentage denominator is zero')
    return f'{(right/left-1)*100:+.2f}%'


def metric_row(report, label=None):
    b, c = report['versions']
    row = [label or workload(report)]
    for metric, digits, divisor in [('t_s', 6, 1), ('b_bytes', 3, MIB), ('m_bytes', 3, MIB)]:
        for phase in ('write', 'query'):
            row.append(arrow(b[phase][metric], c[phase][metric], digits, divisor))
    row.append(arrow(b['local_E']['median'], c['local_E']['median']))
    return row


def check_log(path, expected=0):
    text = path.read_text(encoding='utf-8-sig')
    metadata = [json.loads(line) for line in text.splitlines() if line.startswith('{')]
    require(metadata and metadata[-1]['exit_code'] == expected,
            f'{path.name}: missing expected exit code {expected}')
    return text


def validate_comparison(report, results):
    require(report['rounds'] == 5 and report['complete_interleaving'] is True,
            f"{report['file']}: incomplete five-round comparison")
    require(report['reference_bad'] == 0, f"{report['file']}: truth failure")
    versions = report['versions']
    require(len(versions) == 2 and versions[0]['name'] != versions[1]['name'],
            f"{report['file']}: invalid comparison pair")
    for key in ('rows', 'dim', 'queries', 'seed', 'data'):
        report['workload'][key]
    for version in versions:
        require(version['checked_queries'] == 5 * report['workload']['queries'],
                f"{report['file']}: truth count mismatch")
        require(len(version['local_E']['rounds']) == 5, 'missing round E values')
        finite(version['local_E']['median'], 'E median')
        for value in version['local_E']['rounds']:
            finite(value, 'round E')
        for phase in ('write', 'query'):
            for metric in ('t_s', 'b_bytes', 'm_bytes', 'warmup_s'):
                finite(version[phase][metric], f'{phase} {metric}')
            for metric in ('t_s', 'b_bytes', 'm_bytes'):
                require(len(report['paired_candidate_over_base'][phase][metric]) == 5,
                        'missing paired rounds')
                for value in report['paired_candidate_over_base'][phase][metric]:
                    finite(value, 'paired ratio')
    raw = json_lines(results / report['file'])
    names = [v['name'] for v in versions]
    expected = [(r, name, phase) for r in range(5)
                for name in (names if r % 2 == 0 else names[::-1])
                for phase in ('write', 'query')]
    require([(r['round'], r['name'], r['phase']) for r in raw] == expected,
            f"{report['file']}: raw interleaving does not match summary")
    require(all(all(r[k] == v for k, v in report['workload'].items()) for r in raw),
            f"{report['file']}: raw workload does not match summary")
    require(all(r['reference_bad'] == 0 for r in raw), 'raw truth failure')
    queries = [r for r in raw if r['phase'] == 'query']
    require(all(r['reference_checked'] == report['workload']['queries'] for r in queries),
            'raw query count mismatch')
    require({r['response_hash'] for r in queries} == {report['response_hash']},
            'response hash mismatch')
    run = read_json((results / report['file']).with_name(Path(report['file']).stem + '-run.json'))
    require(run['exit_code'] == 0, 'comparison process failed')


def diagnostic_rows(results):
    paths = [results / 'base-query-diagnostic.txt'] + sorted(results.glob('*-query-work.txt'))
    pattern = re.compile(r'filtered=(false|true) queries=(\d+) exact_rows_per_query=([\d.]+) '
                         r'allocs_per_query=([\d.]+) allocated_bytes_per_query=([\d.]+)')
    rows, links, truth_total = [], [], 0
    for path in paths:
        text = path.read_text(encoding='utf-8-sig')
        require('--- PASS: TestRound6QueryDiagnostic' in text and '\nFAIL' not in text,
                f'{path.name}: diagnostic not passed')
        matches = pattern.findall(text)
        require(len(matches) == 2 and {m[0] for m in matches} == {'false', 'true'},
                f'{path.name}: missing filtered/unfiltered diagnostics')
        truth = re.findall(r'independent_oracle_queries=(\d+)', text)
        require(len(truth) == 1, f'{path.name}: missing diagnostic truth count')
        truth_total += int(truth[0])
        name = path.name.replace('-query-work.txt', '').replace('-query-diagnostic.txt', '')
        for filtered, count, exact, allocs, allocated in matches:
            require(int(count) > 0, 'empty diagnostic')
            rows.append([name, '分类过滤' if filtered == 'true' else '无过滤',
                         f'{float(exact):.2f}', f'{float(allocs):.3f}', f'{float(allocated):.1f}'])
        links.append(evidence(path.name, name))
    return rows, links, truth_total


def generate(results, root):
    selection = read_json(results / 'selection.json')
    selected, rationale = selection['selected'], selection['rationale']
    decisions, commits = selection['decisions'], selection['source_commits']
    require(isinstance(selected, str) and re.fullmatch(r'[A-Za-z0-9_-]+', selected),
            'invalid selected candidate')
    require(isinstance(rationale, str) and rationale.strip(), 'empty selection rationale')
    require(isinstance(decisions, dict) and decisions, 'missing candidate decisions')
    require(isinstance(commits, list) and all(isinstance(c, str) for c in commits),
            'source_commits must be a list of commit strings')
    env = read_json(results / 'environment.json')
    require(env['baseline_commit'] == BASELINE, 'unexpected baseline')
    require(env['official_submissions'] == 0 and env['pushed'] is False,
            'report wording requires no push and no formal submission')
    require(env['platform_score_commit_verified'] is False and
            env['platform_cpu_and_TBM_available'] is False, 'platform evidence changed')
    platform_score = finite(env['user_reported_latest_platform_score'], 'user reported score')
    require(env['cgroup']['memory.max'] == 2 << 30 and env['cgroup']['memory.swap.max'] == 0,
            'unexpected memory limit')
    require(env['cgroup']['cpu.max'] == '200000 100000', 'unexpected CPU limit')
    cpu = re.search(r'^model name\s*:\s*(.+)$', env['cpu_info'], re.MULTILINE)
    require(cpu is not None, 'missing CPU model')
    all_reports = read_json(results / 'all-comparisons.json')
    require(isinstance(all_reports, list) and all_reports, 'all-comparisons must be a nonempty list')
    require(len({r['file'] for r in all_reports}) == len(all_reports), 'duplicate comparisons')
    for report in all_reports:
        validate_comparison(report, results)
    base_reports = [r for r in all_reports if base_name(r['versions'][0]['name']) == 'base']
    retained = [r for r in base_reports if base_name(r['versions'][1]['name']) == selected]
    require(retained, 'selected candidate has no base comparison')
    independent = [r for r in base_reports if base_name(r['versions'][1]['name']) != selected]
    ablations = [r for r in all_reports if
                 base_name(r['versions'][0]['name']) == base_name(r['versions'][1]['name']) and
                 'MVDB_NO_BOOTSTRAP=1' in r['versions'][0]['name'] and
                 'MVDB_NO_BOOTSTRAP=1' not in r['versions'][1]['name']]
    require(ablations, 'missing same-binary bootstrap ablation')
    conditional = [r for r in all_reports if r not in base_reports and r not in ablations]
    for name in {base_name(r['versions'][1]['name']) for r in base_reports}:
        require(isinstance(decisions[name], str) and decisions[name].strip(),
                f'missing decision for {name}')
    binary_hash = read_json(results / f'{selected}-binary-sha256.json')[selected]
    require(re.fullmatch(r'[0-9a-f]{64}', binary_hash), 'invalid selected binary SHA256')

    test_links = []
    for suffix in ('tests', 'fallback-tests', 'race', 'exact-edges', 'protocol'):
        path = results / f'{selected}-{suffix}.txt'
        check_log(path)
        test_links.append(evidence(path.name, suffix))
    crash = check_log(results / f'{selected}-crash.txt')
    crash_count = re.findall(r'ok: (\d+) rounds, (\d+) committed rows', crash)
    require(len(crash_count) == 1 and int(crash_count[0][0]) == 30, 'missing 30-round crash result')
    protocol_path = results / f'{selected}-protocol.json'
    if not protocol_path.exists():
        protocol_path = root / f'{selected}-protocol/result.json'
    protocol = read_json(protocol_path)
    require(protocol['scope'] == 'public-protocol-check' and protocol['passed'] is True,
            'protocol check not passed')
    require(protocol['cases'] and all(c['status'] == 'PASS' for c in protocol['cases']),
            'incomplete protocol cases')
    protocol_count = len(protocol['cases'])

    failed_text = check_log(results / 'bootstrap-tests.txt', expected=1)
    failed_names = re.findall(r'^--- FAIL: (\S+)', failed_text, re.MULTILINE)
    require(failed_names, 'failed bootstrap log has no failing test')
    check_log(results / 'bootstrap-tests-fixed.txt')
    diag_rows, diag_links, diag_truth = diagnostic_rows(results)
    selfcheck = read_json(results / 'stress-validator-selfcheck.json')
    require(selfcheck['recorded_success_accepted'] is True and
            selfcheck['new_capacity_run'] is False, 'stress validator replay not passed')
    rejected = selfcheck['corrupted_records_rejected']
    require(set(rejected) == {'missing_success', 'wrong_truth', 'wrong_peak_scope',
                             'fractional_count', 'oom'} and
            all(isinstance(reason, str) and reason.strip() for reason in rejected.values()),
            'stress validator corruption checks incomplete')
    replay_source = selfcheck['source']
    require(isinstance(replay_source, str) and
            replay_source.replace('\\', '/').endswith('/round3/stress-final.jsonl'),
            'unexpected validator replay source')

    capacity = read_json(results / f'{selected}-capacity-summary.json')
    capacity_run = read_json(results / f'{selected}-capacity-run.json')
    require(capacity_run['exit_code'] == 0 and capacity['single_version_capacity_check'] is True
            and capacity['no_comparison_E'] is True, 'incomplete capacity run')
    cv = capacity['validation']
    require(cv['phase'] == 'success' and cv['reference_bad'] == 0 and
            cv['memory_events']['oom'] == cv['memory_events']['oom_kill'] == 0,
            'capacity validation failed')
    require(cv['original_fixture_metadata_unchanged'] is True and
            re.fullmatch(r'[0-9a-f]{64}', cv['fixture_sha256_unchanged']), 'fixture protection missing')
    cap_rows = []
    for suite in capacity['validated_service_totals']:
        require(len(suite['runs']) == 1, 'capacity report expects single-version one-round runs')
        run = suite['runs'][0]
        service = [p for p in capacity['phases'] if
                   p['phase'] in {suite['suite'] + '_' + suffix
                                  for suffix in ('startup', 'query', 'shutdown')}]
        require(len(service) == 3 and len({p['phase'] for p in service}) == 3,
                'missing capacity service telemetry')
        query = [p for p in service if p['phase'] == suite['suite'] + '_query']
        require(len(query) == 1, 'missing capacity query telemetry')
        q, total, telemetry = run['query_only'], run['measured_stage_total'], query[0]
        cap_rows.append([suite['suite'], f"{q['t_s']:.6f}", f"{q['b_bytes']/MIB:.3f}",
                         f"{q['m_bytes']/MIB:.3f}", f"{total['t_s']:.6f}",
                         f"{total['b_bytes']/MIB:.3f}", f"{total['m_bytes']/MIB:.3f}",
                         f"{telemetry['max_sampled_anon']/MIB:.3f}",
                         f"{telemetry['max_sampled_file']/MIB:.3f}",
                         f"{max(p['max_sampled_anon'] for p in service)/MIB:.3f}",
                         f"{max(p['max_sampled_file'] for p in service)/MIB:.3f}"])
    require({r[0] for r in cap_rows} == {'point', 'order', 'census'}, 'capacity suites missing')

    stress = read_json(results / f'{selected}-stress-summary.json')
    stress_run = read_json(results / f'{selected}-stress-run.json')
    require(stress['validation_passed'] is True and stress_run['validation_passed'] is True and
            stress_run['exit_code'] == 0 and stress_run['candidate_sha256'] == binary_hash,
            'selected binary stress evidence is incomplete')
    require(stress['per_phase_peak_reset_verified'] is True and stress['no_comparison_E'] is True,
            'stress measurement validation missing')
    require(stress['success']['memory_events']['oom'] == stress['success']['memory_events']['oom_kill'] == 0,
            'stress OOM')
    stress_rows, census_number = [], 0
    for record in stress['phases']:
        phase = record['phase']
        if phase == 'final_full_census':
            census_number += 1
            phase += f' #{census_number}'
        stress_rows.append([phase, f"{record['t_s']:.6f}", f"{record['b_bytes']/MIB:.3f}",
                            f"{record['m_bytes']/MIB:.3f}",
                            f"{record['max_sampled_anon']/MIB:.3f}",
                            f"{record['max_sampled_file']/MIB:.3f}"])

    selected_normal = sum(v['checked_queries'] for r in all_reports for v in r['versions']
                          if v['name'] == selected)
    selected_switched = sum(v['checked_queries'] for r in all_reports for v in r['versions']
                            if base_name(v['name']) == selected and v['name'] != selected)
    query_ratios = [x for r in all_reports for x in r['paired_candidate_over_base']['query']['t_s']]
    differing_b = sum(any(x != 1 for x in r['paired_candidate_over_base']['write']['b_bytes'])
                      for r in all_reports)
    worse_e = [workload(r) for r in retained
               if r['versions'][1]['local_E']['median'] < r['versions'][0]['local_E']['median']]
    slower_query = [workload(r) for r in retained
                    if r['versions'][1]['query']['t_s'] > r['versions'][0]['query']['t_s']]
    headers = ['负载 / 每轮查询', '写入 T 秒', '查询 T 秒', '写入 B MiB', '查询 B MiB',
               '写入 M MiB', '查询 M MiB', '综合 E']
    text = [
        f'本轮从 `{BASELINE}` 开始，只比较当前基线与本轮候选。最终选择 **`{selected}`**：{rationale}',
        f'用户报告最近平台分数为 **{platform_score:g}**，对应提交 SHA 尚未核实，平台 CPU 与分项 T/B/M 也未取得。'
        '本报告不把该分数绑定到本轮基线，不将本地 E 换算成正式分数，也不据此声称接近或达到 5500。'
        '本轮未推送、未正式提测。',
        f"环境为 {cpu.group(1)}、{env['compiler']}、{env['kernel']}。数据库限制为 2 CPU、2GiB、无 swap，"
        'COMMIT 保持 fdatasync，独立真值在 cgroup 外计算；性能负载串行执行。'
        '查询 T 排除装载及首条预热，B/M 包含装载、预热、测量和退出写回；M 是 cgroup 峰值，包含文件缓存。',
        '所有普通对照均为五轮正反交错。下表箭头为同组参考→候选的中位数，保留全部负收益组。'
        '本地 E 使用 T/B/M=0.6/0.2/0.2、写入/查询=1/2、1ms/1MiB/1MiB 平滑，参考取该组参考版前三轮中位数，'
        '逐轮计算 E 后再取中位数。不同参考组的 E 不混合。',
        '**选定版本的全部基线对照**',
        table(headers, [metric_row(r) for r in retained]),
        '综合 E 下降的组：' + ('；'.join(worse_e) if worse_e else '本次已完成对照中没有') + '。'
        '查询 T 中位数变慢的组：' + ('；'.join(slower_query) if slower_query else '本次已完成对照中没有') + '。'
        '写入波动仍计入 E，不能自动归因于只修改查询路径的优化。',
        '**候选选择与独立实验**',
        table(['候选', '决定及理由'], [[name, reason] for name, reason in decisions.items()]),
    ]
    if independent:
        rows = []
        for report in independent:
            b, c = report['versions']
            rows.append([c['name'], workload(report), percent(b['write']['t_s'], c['write']['t_s']),
                         percent(b['query']['t_s'], c['query']['t_s']),
                         arrow(b['query']['m_bytes'], c['query']['m_bytes'], 3, MIB),
                         arrow(b['local_E']['median'], c['local_E']['median'])])
        text.append(table(['候选', '负载', '写入 T 变化', '查询 T 变化', '查询 M MiB', '综合 E'], rows))
    if conditional:
        text.extend([
            '**保持其他优化后的直接对照**',
            '以下完整保留所有非 base、非同二进制 bootstrap 开关对照，包括固定 typed heap 后的 '
            '`heap→wide`。`wide` 仅在维数至少 256 时启用 bootstrap，未包含 flatrows；'
            '每组箭头对应实际运行的两个版本，不把无关路径或负收益组排除。',
            table(headers, [metric_row(r, r['versions'][0]['name'] + '→' + r['versions'][1]['name'] +
                                        ' · ' + workload(r)) for r in conditional]),
        ])
    text.extend([
        '候选摘要保留本轮所有其他版本的 base 对照；组合候选与单项候选的身份由上表决定说明区分。'
        '全部逐轮 T/B/M、响应哈希、真值数量及其他条件对照见 ' + evidence('all-comparisons.json') + '。',
        '**bootstrap 在同一二进制中的消融**',
        '下表仅切换 `MVDB_NO_BOOTSTRAP=1`，箭头为禁用→启用。代码布局相同，仍按完整写入/查询 T/B/M 判断；'
        '减少精算行数不能替代端到端收益。',
        table(headers, [metric_row(r, base_name(r['versions'][1]['name']) + ' · ' + workload(r)) for r in ablations]),
        '**机制诊断与保留的失败记录**',
        table(['诊断副本', '查询类型', '精算行/查询', '分配次数/查询', '累计分配 B/查询'], diag_rows),
        '这些计数来自临时插桩副本，分别统计扫描工作与累计分配，不是峰值 M 或性能分。'
        f'诊断独立校验 {diag_truth:,} 条查询，后续重复计数循环不重复计入真值。证据：' + '、'.join(diag_links) + '。',
        'bootstrap 首次完整测试失败于 `' + '、'.join(failed_names) + '`：测试把分类 c1 更新成原值 c1，'
        '没有生成事务 overlay，却断言必须回退。改为实际更新到 c2 后，完整测试通过。'
        '原始失败和修正后的记录均保留：' + evidence('bootstrap-tests.txt') + '、' +
        evidence('bootstrap-tests-fixed.txt') + '。报告不把失败运行算作通过。',
        '压力记录验证器以 ROUND3 的成功记录重放通过，并拒绝缺少成功标记、真值错误、峰值作用域错误、'
        '小数计数和 OOM 五类损坏记录。这是验证器自检，未运行新的容量负载，不计入本轮新真值。证据：' +
        evidence('stress-validator-selfcheck.json') + '。',
        f'所有 {len(all_reports)} 组五轮对照原样保留，没有删除较差轮次。'
        f'全体逐轮配对查询 T 比值范围为 {min(query_ratios):.3f}–{max(query_ratios):.3f}；'
        f'{differing_b} 组的写入物理 B 至少一轮不同。短对照中的波动以及与改动路径无关的写入变化均保留，'
        '不把有利中位数解释成所有轮次都获胜；详见逐轮配对比率和原始 JSONL。',
        '**2GiB 限制下的既有大日志容量确认**',
        f"选定版本通过 {cv['reference_checked']} 条 SQL、{cv['reference_rows_checked']:,} 行独立校验。"
        '下表是单版本容量检查，没有对照 E。服务合计为启动＋查询＋退出，排除准备、冷页驱逐和外部真值；'
        '每次启动及 ready 后查询前均验证私有 fixture 冷页，源元数据与私有日志哈希保持一致。',
        table(['SQL 组', '查询 T 秒', '查询 B MiB', '查询 M MiB', '服务 T 秒', '服务 B MiB',
               '服务最大 M MiB', '查询采样 anon MiB', '查询采样 file MiB',
               '服务采样 anon MiB', '服务采样 file MiB'], cap_rows),
        evidence(f'{selected}-capacity-summary.json') + '；census 验证主键、分类和独立范数，'
        '不等于返回全部向量并逐元素比较。',
        '**本轮完整大数据写入、修改与恢复**',
        f"新写入 {stress['config']['rows']:,}×{stress['config']['dim']} float32，原始向量 "
        f"{stress['config']['raw_vector_bytes']:,}B，大于 2GiB。完成全表 UPDATE、"
        f"{stress['sigkill_events']} 次 SIGKILL、{stress['independent_topk_queries']} 条独立 TopK、"
        f"{stress['census_rows_checked']:,} 行 census 和 {stress['transaction_point_rows_checked']} 次事务点查。"
        f"未提交日志尾部 {stress['uncommitted_tail_bytes']:,}B 已实际写入文件并在恢复后精确截断。",
        table(['阶段', 'T 秒', 'B MiB', 'M MiB', '采样最大 anon MiB', '采样最大 file MiB'], stress_rows),
        f"全流程内核峰值 {stress['whole_workflow_peak_bytes']/MIB:.3f}MiB，所有阶段 peak FD 重置校验通过，"
        'oom=0、oom_kill=0，cgroup 跨重启复用。内核记录峰值可能略高于配置上限；'
        'anon/file 是不同时间的采样最大值，不能相加，也不能把文件缓存算作 Go 堆。'
        '完整记录：' + evidence(f'{selected}-stress-summary.json') + '。'
        '同一时刻的峰值样本与源码位置分析见 ' + evidence('memory-analysis.md') + '。',
        '恢复强杀已确认发生在 ready 之前；驱动没有内部恢复进度钩子。完整流程没有逐请求超时，'
        '不能称为正式时限通过。census 不覆盖全量向量投影的物化内存，当前容量结论限于实际执行的查询。',
        '**真值、功能验证与可复现版本**',
        f"选定二进制正常运行模式在全部已完成性能对照中独立校验 **{selected_normal:,}** 条 TopK；"
        f"带显式禁用开关的对照另有 **{selected_switched:,}** 条。"
        f"完整压力流程另校验 {stress['independent_topk_queries']} 条 TopK，容量 SQL 与临时插桩诊断分别记账，"
        '未把其他候选的查询数计入选定版本。所有计入的查询均完成独立真值，不只是比较响应哈希。',
        '选定版本完整 Go 测试、禁用 AVX-VNNI 回退、engine/types race、exact 边界均通过；'
        f'随机崩溃恢复 {crash_count[0][0]} 轮、{int(crash_count[0][1]):,} 条已提交行通过；'
        f'本地 native 公共协议 {protocol_count}/{protocol_count} PASS。证据：' + '、'.join(test_links) + '、' +
        evidence(f'{selected}-crash.txt') + '。这些均不是平台回调或正式成绩。',
        f'选定二进制 SHA256：`{binary_hash}`。生产提交：' +
        ('、'.join(f'`{commit}`' for commit in commits) if commits else '尚未在 selection.json 登记') + '。'
        '选择记录见 ' + evidence('selection.json') + '，原始测量与未保留候选继续归档；本轮未推送、未正式提测。',
    ])
    return '\n\n'.join(text) + '\n'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--results', type=Path, default=RESULTS)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--output', type=Path, default=ROOT / 'ROUND6-draft.md')
    args = parser.parse_args()
    report = generate(args.results, args.root)
    with args.output.open('x', encoding='utf-8', newline='\n') as stream:
        stream.write(report)
    print(args.output)


if __name__ == '__main__':
    main()
