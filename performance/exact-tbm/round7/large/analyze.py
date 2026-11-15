"""Validate all five interleaved large exact trials; retain every measured round.

This reports local query-service T/B/M and stage E, never a write/query overall
score or a predicted platform score. It does not execute database code.
"""
import argparse
import hashlib
import json
import math
from pathlib import Path
import re
import statistics


METRICS = ('t_s', 'b_bytes', 'm_bytes')
WEIGHTS = (('t_s', .6, .001), ('b_bytes', .2, 1 << 20), ('m_bytes', .2, 1 << 20))
STAGES = ('startup', 'cold', 'replay', 'shutdown')
SCOPES = STAGES + ('service_total',)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def finite(value, label):
    require(isinstance(value, (int, float)) and not isinstance(value, bool)
            and math.isfinite(value) and value >= 0, f'invalid {label}: {value!r}')
    return value


def integer(value, label, minimum=0):
    require(type(value) is int and value >= minimum, f'invalid {label}: {value!r}')
    return value


def sha(value, label):
    require(isinstance(value, str) and re.fullmatch('[0-9a-f]{64}', value),
            f'invalid {label}')
    return value


def read_json(path):
    return json.loads(path.read_text(encoding='utf-8-sig'))


def check_events(record):
    events = record['memory_events']
    require(type(events['oom']) is int and type(events['oom_kill']) is int and
            events['oom'] == events['oom_kill'] == 0, 'OOM or missing valid OOM counters')
    for value in events.values():
        integer(value, 'memory.events counter')


def check_metrics(record):
    require(record['memory_peak_scope'] == 'phase_reset_fd', 'phase peak reset not verified')
    require(record['sample_period_ms'] == 50, 'unexpected sampling period')
    for metric in ('t_s', 'wall_s', 'm_bytes', 'b_bytes', 'r_bytes', 'w_bytes',
                   'max_sampled_anon', 'max_sampled_file', 'max_sampled_rss'):
        finite(record[metric], metric)
    require(record['m_bytes'] > 0 and record['wall_s'] + 1e-9 >= record['t_s'],
            'invalid phase memory or wall time')
    require(record['b_bytes'] == record['r_bytes'] + record['w_bytes'], 'physical I/O total mismatch')
    integer(record['pid'], 'database PID', 1)
    require(isinstance(record['device'], str) and re.fullmatch(r'\d+:\d+', record['device']),
            'missing data-device accounting')
    check_events(record)


def option(argv, key, default=None):
    result = default
    for i, token in enumerate(argv):
        stripped = token.lstrip('-')
        if token.startswith('-') and stripped == key:
            require(i + 1 < len(argv), f'missing argv value for {key}')
            result = argv[i + 1]
        elif token.startswith('-') and stripped.startswith(key + '='):
            result = stripped[len(key) + 1:]
    return result


def validate(path, run_path, allow_small=False):
    run = read_json(run_path)
    require(type(run['exit_code']) is int and run['exit_code'] == 0, 'driver did not exit successfully')
    argv = run['argv']
    require(isinstance(argv, list) and argv and all(isinstance(x, str) for x in argv), 'missing driver argv')
    records = [json.loads(line) for line in path.read_text(encoding='utf-8-sig').splitlines() if line.strip()]
    require(records and records[0].get('phase') == 'config', 'missing initial config')
    require(records[-1].get('phase') == 'success', 'incomplete run; missing final success')
    require(not any(r.get('phase') in {'failure', 'failed_phase', 'failed_request', 'process_read_failure'}
                    for r in records), 'failed record present')
    for record in records:
        for key in ('round', 'position', 'queries', 'unique_oracle_queries', 'reference_bad',
                    'reference_checked', 'reference_rows_checked', 'completed_trials',
                    'process_exit_code', 'shutdown_request_id', 'private_copies'):
            if key in record:
                integer(record[key], key)
        require(record.get('reference_bad', 0) == 0, 'truth mismatch')
        if 'memory_events' in record:
            check_events(record)

    config, definition, fixture = records[:3]
    require(config['mode'] == 'round7_large_topk_interleaved', 'wrong driver mode')
    require(config['rounds'] == 5 and type(config['rounds']) is int, 'need exactly five rounds')
    rows = integer(config['rows'], 'rows', 100)
    dim = integer(config['dim'], 'dim', 1)
    live = integer(config['live_rows'], 'live_rows', 1)
    require(live == rows - 3, 'wrong final live-row count')
    require(config['fixture_model'] == 'completed_final_all_c9', 'unexpected fixture model')
    require(config['raw_vector_bytes'] == rows * dim * 4 and
            config['live_vector_bytes'] == live * dim * 4, 'vector byte count mismatch')
    exceeds = live * dim * 4 > 2 << 30
    require(config['exceeds_2gib'] is exceeds, 'incorrect capacity claim')
    require(type(config['allow_small']) is bool, 'missing smoke-mode flag')
    require(exceeds or (allow_small and config['allow_small']), 'live vectors do not exceed 2GiB')
    require(config['memory_limit'] == 2 << 30 and config['memory_swap_max'] == 0 and
            config['cpu_max'] == '200000 100000' and config['pids_max'] == 128,
            'unexpected database resource limits')
    require(config['index_mem_mb'] == 256 and config['sync'] == 'fdatasync' and
            config['search_mode'] == 'exact', 'unexpected index budget, durability or search mode')
    require(config['database_env'] == ['GOMAXPROCS=2', 'GOMEMLIMIT=1288490188', 'GOGC=20'] and
            config['inherited_mvdb_env_removed'] is True, 'unexpected database environment')
    count = integer(config['unique_oracle_queries'], 'unique oracle queries', 1)
    require(count in (6, 12) and config['queries_per_batch'] == count and config['top_k'] == 10,
            'unexpected query count or K')
    bins = config['bins']
    require(isinstance(bins, list) and len(bins) == 2 and
            all(isinstance(b, str) and b.startswith('/') for b in bins), 'need two absolute binaries')
    names = [Path(b).name for b in bins]
    require(len(set(names)) == 2, 'binary basenames must differ')
    binary_hashes = config['binary_sha256']
    require(set(binary_hashes) == set(bins), 'binary hashes missing')
    for binary in bins:
        sha(binary_hashes[binary], 'binary hash')
    for key, expected, default in (
        ('bins', ','.join(bins), None), ('fixture', config['fixture_source'], None),
        ('work-dir', config['work_dir'], None), ('rows', str(rows), '550000'),
        ('dim', str(dim), '1024'), ('queries', str(count), '6'),
    ):
        require(option(argv, key, default) == expected, f'run argv/config mismatch for {key}')

    require(definition['phase'] == 'workload_definition', 'missing independent workload definition')
    statements, labels = definition['statements'], definition['labels']
    require(len(statements) == len(set(statements)) == len(labels) == len(set(labels)) == count,
            'missing or duplicated SQL cases')
    require(all(isinstance(s, str) for s in statements + labels), 'invalid SQL/labels')
    sql_hash = sha(definition['sql_sha256'], 'SQL hash')
    require(hashlib.sha256('\n'.join(statements).encode()).hexdigest() == sql_hash, 'SQL hash mismatch')
    truth_hash = sha(definition['truth_sha256'], 'oracle hash')
    expected_rows = definition['expected_rows']
    require(len(expected_rows) == count, 'missing oracle cases')
    deleted = {rows // 3, rows // 2, rows - 1}
    for expected in expected_rows:
        require(len(expected) == 10, 'oracle K mismatch')
        previous = None
        seen = set()
        for row in expected:
            require(len(row) == 3, 'oracle row width mismatch')
            pk = integer(row[0], 'oracle primary key', 1)
            distance = finite(row[2], 'oracle distance')
            require(pk <= rows and pk not in deleted and pk not in seen and row[1] == 'c9',
                    'invalid or duplicate oracle row')
            pair = (distance, pk)
            require(previous is None or pair > previous, 'oracle tie order invalid')
            previous = pair
            seen.add(pk)
    encoded_truth = json.dumps(expected_rows, ensure_ascii=False, separators=(',', ':'), allow_nan=False).encode()
    require(hashlib.sha256(encoded_truth).hexdigest() == truth_hash, 'oracle definition hash mismatch')
    require(definition['unique_oracle_queries'] == count and definition['oracle_rows_per_query'] == live,
            'oracle did not cover the complete live table')
    finite(definition['oracle_s'], 'oracle time')

    require(fixture['phase'] == 'fixture_ready' and fixture['distinct_inode_verified'] is True and
            fixture['private_copies'] == 1, 'private fixture preparation invalid')
    source_hash = sha(fixture['source_sha256'], 'source fixture hash')
    private_hash = sha(fixture['private_sha256'], 'private fixture hash')
    require(source_hash == private_hash, 'private copy differs from source')
    fixture_bytes = integer(fixture['fixture_bytes'], 'fixture bytes', rows * dim * 4)
    private_log = str(Path(config['work_dir']) / 'db/data.log')
    require(fixture['private_log'] == private_log and private_log != config['fixture_source'],
            'unexpected private fixture path')
    for key in ('source_signature', 'private_signature'):
        require(isinstance(fixture[key], str) and re.fullmatch(r'\d+:\d+', fixture[key]),
                'missing fixture metadata signature')
        require(int(fixture[key].split(':')[0]) == fixture_bytes, 'fixture signature size mismatch')

    cursor = 3
    trials = {name: [] for name in names}
    response_hash = None
    data_device = None
    total_checked = 0
    for round_ in range(5):
        order = bins if round_ % 2 == 0 else bins[::-1]
        for position, binary in enumerate(order):
            name = Path(binary).name
            context = dict(name=name, binary=binary, binary_sha256=binary_hashes[binary],
                           round=round_, position=position, sql_sha256=sql_hash,
                           truth_sha256=truth_hash, queries=count)

            def take(phase):
                nonlocal cursor
                require(cursor < len(records), f'missing {phase}')
                record = records[cursor]
                cursor += 1
                require(record.get('phase') == phase,
                        f'raw order mismatch at {cursor-1}: expected {phase}, got {record.get("phase")}')
                for key, value in context.items():
                    require(record[key] == value and type(record[key]) is type(value),
                            f'{phase}: incorrect {key}; interleaving or workload changed')
                return record

            def cache_record(stage):
                record = take('fixture_cache_evicted')
                require(record['stage'] == stage and record['path'] == private_log and
                        record['method'] == 'file_sync_then_posix_fadvise_dontneed' and
                        record['file_bytes'] == fixture_bytes, 'wrong cache-eviction scope')
                resident = integer(record['resident_pages'], 'resident pages')
                page_size = integer(record['page_size'], 'page size', 1)
                require(resident <= 1 and record['total_pages'] == (fixture_bytes+page_size-1)//page_size,
                        'fixture was not cold or residency accounting changed')
                return resident

            take('trial_begin')
            before_start = cache_record('before_startup')
            startup = take('startup')
            require(startup['startup_meter_started_before_exec'] is True and
                    startup['startup_wall_s'] == startup['t_s'] and
                    startup['resident_pages_before_startup'] == before_start,
                    'startup timing or cold scope incomplete')
            before_cold = cache_record('before_cold')
            phases = {'startup': startup}
            for batch in ('cold', 'replay'):
                record = take(batch)
                require(record['batch'] == batch, 'batch role mismatch')
                ids = list(range(1, count+1)) if batch == 'cold' else list(range(count+1, 2*count+1))
                require(record['request_ids'] == ids and all(type(x) is int for x in record['request_ids']),
                        'request IDs are not the actual ordered protocol sequence')
                require(record['reference_checked'] == count and record['reference_rows_checked'] == count*10,
                        'not every query/row was independently checked')
                require(len(record['per_query_t_s']) == count, 'missing per-query timing')
                for value in record['per_query_t_s']:
                    finite(value, 'query duration')
                require(sum(record['per_query_t_s']) <= record['t_s'] + 1e-9, 'batch timing excludes query work')
                if batch == 'cold':
                    require(record['cache_state'] == 'cold_at_batch_start' and
                            record['resident_pages_before_batch'] == before_cold, 'cold-batch scope changed')
                else:
                    require(record['cache_state'] == 'same_process_no_eviction' and
                            record['predecessor_batch'] == 'cold', 'replay setup changed')
                digest = sha(record['response_sha256'], 'normalized response hash')
                if response_hash is None:
                    response_hash = digest
                require(digest == response_hash, 'responses differ between versions, rounds or batches')
                validation = take('batch_validation')
                require(validation['batch'] == batch and validation['request_ids'] == ids and
                        validation['reference_checked'] == count and validation['reference_rows_checked'] == count*10 and
                        validation['response_sha256'] == digest and validation['validation'],
                        'independent validation record mismatch')
                total_checked += count
                phases[batch] = record
            shutdown = take('shutdown')
            require(type(shutdown['process_exit_code']) is int and shutdown['process_exit_code'] == 0 and
                    shutdown['shutdown_request_id'] == 2*count+1, 'database did not exit cleanly')
            phases['shutdown'] = shutdown
            for phase, record in phases.items():
                check_metrics(record)
                require(record['pid'] == startup['pid'], f'{phase}: process changed within a trial')
                if data_device is None:
                    data_device = record['device']
                require(record['device'] == data_device, 'data device changed')
            completed = take('trial_complete')
            require(type(completed['process_exit_code']) is int and completed['process_exit_code'] == 0 and
                    completed['private_metadata_unchanged'] is True and completed['source_metadata_unchanged'] is True and
                    completed['reference_checked'] == 2*count and completed['reference_rows_checked'] == 2*count*10,
                    'trial completion evidence missing')
            phases['service_total'] = {
                't_s': sum(phases[p]['t_s'] for p in STAGES),
                'b_bytes': sum(phases[p]['b_bytes'] for p in STAGES),
                'm_bytes': max(phases[p]['m_bytes'] for p in STAGES),
                **{key: max(phases[p][key] for p in STAGES)
                   for key in ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')},
            }
            trials[name].append(dict(round=round_, position=position, phases=phases))

    require(cursor == len(records)-1, 'unexpected or missing records after completed trials')
    final = records[cursor]
    require(final['phase'] == 'success' and final['completed_trials'] == 10 and
            final['reference_checked'] == total_checked == 20*count and
            final['reference_rows_checked'] == total_checked*10 and
            final['unique_oracle_queries'] == count, 'final validation totals mismatch')
    require(final['source_sha256_unchanged'] == source_hash and final['private_sha256_unchanged'] == private_hash and
            final['source_metadata_unchanged'] is True and final['private_metadata_unchanged'] is True and
            final['binary_sha256_unchanged'] == binary_hashes and final['private_copies'] == 1 and
            final['cgroup_reused_across_restarts'] is True and final['response_sha256'] == response_hash,
            'final immutability or restart accounting missing')
    return config, definition, fixture, trials, total_checked


def stage_e(metrics, reference):
    score = 0.0
    for metric, weight, epsilon in WEIGHTS:
        ratio = (reference[metric]+epsilon)/(metrics[metric]+epsilon)
        score += weight*ratio/(1+ratio)
    return score


def summarize(path, run_path, allow_small=False):
    config, definition, fixture, trials, checked = validate(path, run_path, allow_small)
    names = [Path(b).name for b in config['bins']]
    baseline = names[0]
    versions = []
    for name in names:
        scopes = {}
        for scope in SCOPES:
            runs = [dict(round=r['round'], position=r['position'],
                         **{key: r['phases'][scope][key] for key in METRICS},
                         **{key: r['phases'][scope][key] for key in
                            ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')})
                    for r in trials[name]]
            scopes[scope] = {
                'median': {key: statistics.median(r[key] for r in runs) for key in METRICS},
                'sampled_maxima': {key: max(r[key] for r in runs) for key in
                                   ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')},
                'runs': runs,
            }
            if scope in ('cold', 'replay', 'service_total'):
                reference = {key: statistics.median(r['phases'][scope][key] for r in trials[baseline][:3])
                             for key in METRICS}
                scores = [stage_e(r, reference) for r in runs]
                scopes[scope]['stage_E'] = {'baseline': baseline, 'reference_rounds': [0, 1, 2],
                                             'fixed_reference_metrics': reference,
                                             'median': statistics.median(scores), 'runs': scores}
        versions.append({'name': name, 'rounds': 5,
                         'independent_response_checks': 10*config['unique_oracle_queries'],
                         'unique_oracle_queries': config['unique_oracle_queries'], 'scopes': scopes})
    paired = {}
    for scope in SCOPES:
        paired[scope] = []
        for round_ in range(5):
            b, c = [trials[name][round_]['phases'][scope] for name in names]
            paired[scope].append({'round': round_,
                                  **{key: c[key]/b[key] if b[key] else None for key in METRICS}})
    return {
        'validation_passed': True, 'complete_interleaving': True, 'rounds': 5,
        'file': path.name, 'run_metadata': run_path.name, 'workload': config,
        'sql_sha256': definition['sql_sha256'], 'truth_sha256': definition['truth_sha256'],
        'fixture_sha256': fixture['source_sha256'], 'reference_bad': 0,
        'independent_response_checks_total': checked, 'unique_oracle_queries': config['unique_oracle_queries'],
        'versions': versions, 'paired_candidate_over_baseline': paired,
        'limitations': [
            'Local exact query-service T/B/M only; no write-phase weight, overall performance score or platform extrapolation.',
            'Stage E uses T/B/M=0.6/0.2/0.2 with 1ms/1MiB/1MiB smoothing and the first three baseline medians; stages are not combined by invented weights.',
            'service_total adds startup+cold+replay+shutdown T/B and takes maximum M; excludes preparation, cache eviction and external truth/validation.',
            'cold means cold row pages at batch start; later queries may reuse pages and derived state.',
            'replay retains the same process, derived state and remaining page cache; it does not claim the >2GiB vectors are fully resident.',
            'All live categories are c9, so the c9 filter covers the entire table; this is not a selective-category benchmark.',
            'Repeated responses are individually checked against independent truth; unique SQL cases are counted separately.',
            'Sampled anon/file maxima may occur at different instants and cannot be added; M is the retained-FD kernel cgroup peak.',
            'Null paired ratios mean the reference metric is zero; those rounds remain in raw metrics and stage E.',
            'No per-request deadline is enforced; this is not evidence of passing a formal time limit.',
        ],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('path', type=Path)
    parser.add_argument('--run', type=Path, help='default: <input-stem>-run.json')
    parser.add_argument('--allow-small', action='store_true', help='explicitly accept functional smoke data')
    args = parser.parse_args()
    run_path = args.run or args.path.with_name(args.path.stem+'-run.json')
    print(json.dumps(summarize(args.path, run_path, args.allow_small), indent=2, ensure_ascii=False, allow_nan=False))


if __name__ == '__main__':
    main()
