"""Strict validation of fresh-database indexed-update comparisons; no weighted E."""
import argparse
import hashlib
import json
import math
from pathlib import Path
import re
import statistics


METRICS = ('t_s', 'b_bytes', 'm_bytes')
STAGES = ('startup', 'load', 'index_query', 'category_update', 'point_update_front',
          'point_update_tail', 'updated_query', 'updated_census', 'shutdown',
          'recovery', 'recovered_query', 'recovered_census', 'final_shutdown')
MUTATIONS = ('category_update', 'point_update_front', 'point_update_tail')
SCOPES = STAGES + ('indexed_update_ack_total', 'measured_stage_sum')


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


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def validate(path, run_path):
    run = read_json(run_path)
    require(type(run['exit_code']) is int and run['exit_code'] == 0, 'driver failed or did not finish')
    argv = run['argv']
    require(isinstance(argv, list) and argv and all(isinstance(x, str) for x in argv), 'missing argv')
    before, after = run.get('driver_identities_before'), run.get('driver_identities_after')
    require(run.get('driver_identities_verified') is True and isinstance(before, dict) and
            isinstance(after, dict) and before == after, 'driver identity verification missing or failed')
    require(len(before) == 2 and argv[0] in before, 'driver/source identity mapping is incomplete')
    for artifact, value in before.items():
        require(isinstance(artifact, str) and artifact.startswith('/'), 'invalid driver identity path')
        sha(value, 'driver/source identity hash')
    source_paths = [artifact for artifact in before if artifact != argv[0]]
    require(source_paths[0].endswith('/performance/exact-tbm/round7/update/main.go'),
            'indexed-update driver source identity missing')
    records = [json.loads(line) for line in path.read_text(encoding='utf-8-sig').splitlines() if line.strip()]
    require(len(records) >= 5 and records[0].get('phase') == 'config' and
            records[-1].get('phase') == 'success', 'missing config or final success')
    require(not any(r.get('phase') in ('failure', 'failed_phase', 'failed_request', 'process_read_failure')
                    for r in records), 'failed record present')
    count_fields = ('round', 'position', 'reference_bad', 'reference_checked', 'reference_rows_checked',
                    'request_id', 'shutdown_request_id', 'process_exit_code', 'affected_rows_verified',
                    'acknowledged_rows', 'requests', 'last_request_id', 'topk_checked', 'census_queries_checked',
                    'census_rows_checked', 'process_exits_verified', 'completed_trials', 'unique_state_query_oracles')
    for record in records:
        for key in count_fields:
            if key in record:
                integer(record[key], key)
        if record.get('phase') in ('index_query', 'updated_query', 'recovered_query',
                                   'updated_census', 'recovered_census',
                                   'query_validation', 'census_validation'):
            require(type(record.get('reference_bad')) is int and record['reference_bad'] == 0,
                    'truth validation requires an explicit integer-zero reference_bad')
        require(record.get('reference_bad', 0) == 0, 'truth failure')
        if 'memory_events' in record:
            check_events(record)
    config = records[0]
    require(config['mode'] == 'round7_indexed_update_interleaved' and config['rounds'] == 5,
            'wrong mode or incomplete five-round comparison')
    rows, dim, batch = [integer(config[k], k, v) for k, v in (('rows', 100), ('dim', 16), ('batch', 1))]
    budget = integer(config['index_mem_mb'], 'index budget MiB', 1)
    require(budget <= 256 and rows*dim > budget << 20 and rows*64 < budget << 20 and
            config['full_codes_exceed_budget'] is True, 'load does not exercise an insufficient full-code budget')
    require(config['raw_vector_bytes'] == rows*dim*4 and config['full_code_bytes'] == rows*dim and
            config['slot_estimate_bytes'] == rows*64, 'dataset size accounting mismatch')
    require(config['queries_per_batch'] == 6 and config['top_k'] == 10 and config['unique_state_query_oracles'] == 12,
            'unexpected query count or K')
    require(config['memory_limit'] == 2 << 30 and config['memory_swap_max'] == 0 and
            config['cpu_max'] == '200000 100000' and config['pids_max'] == 128 and
            config['sync'] == 'fdatasync' and config['search_mode'] == 'exact', 'resource or durability settings changed')
    require(config['database_env'] == ['GOMAXPROCS=2', 'GOMEMLIMIT=1288490188', 'GOGC=20'] and
            config['inherited_mvdb_env_removed'] is True, 'runtime environment changed')
    require(config['is_over_2gib_capacity_run'] is False, 'indexed-update experiment mislabeled as capacity test')
    bins = config['bins']
    require(len(bins) == 2 and all(isinstance(b, str) and b.startswith('/') for b in bins), 'invalid binary list')
    names = [Path(b).name for b in bins]
    require(len(set(names)) == 2, 'binary basenames must differ')
    hashes = config['binary_sha256']
    require(set(hashes) == set(bins), 'missing binary hashes')
    for value in hashes.values():
        sha(value, 'binary hash')
    for key, expected, default in (
        ('bins', ','.join(bins), None), ('work-dir', config['work_dir'], None),
        ('rows', str(rows), '10000'), ('dim', str(dim), '1024'),
        ('batch', str(batch), '64'), ('index-mem-mb', str(budget), '4'),
    ):
        require(option(argv, key, default) == expected, f'argv/config mismatch for {key}')

    definitions = {}
    expected_response_hashes = {}
    for record, state, category in zip(records[1:3], ('initial', 'updated'), ('c9', 'c8')):
        require(record['phase'] == 'oracle_definition' and record['state'] == state and record['category'] == category,
                'missing initial/updated independent oracle')
        require(record['unique_queries'] == 6 and record['oracle_rows_per_query'] == rows,
                'oracle did not cover every row/query')
        statements, labels = record['statements'], record['labels']
        require(len(statements) == len(set(statements)) == len(labels) == len(set(labels)) == 6,
                'missing or duplicate SQL cases')
        require(sha(record['sql_sha256'], 'SQL hash') == digest('\n'.join(statements)), 'SQL hash mismatch')
        expected = record['expected_topk']
        require(len(expected) == 6, 'oracle case count mismatch')
        for case in expected:
            require(len(case) == 10, 'oracle K mismatch')
            previous, seen = None, set()
            for row in case:
                require(len(row) == 3, 'oracle row width mismatch')
                pk, distance = integer(row[0], 'oracle PK', 1), finite(row[2], 'oracle distance')
                require(pk <= rows and pk not in seen and row[1] == category, 'invalid oracle row')
                pair = distance, pk
                require(previous is None or pair > previous, 'oracle distance/PK tie order mismatch')
                previous = pair
                seen.add(pk)
        encoded = json.dumps(expected, separators=(',', ':'), ensure_ascii=False, allow_nan=False)
        require(sha(record['truth_sha256'], 'truth hash') == digest(encoded), 'truth hash mismatch')
        # The Go driver hashes each canonical response row array followed by
        # one newline. Bind the response to independent expected rows, not
        # merely to another version's response or a repeated trial.
        response_text = ''.join(json.dumps(case, separators=(',', ':'), ensure_ascii=False,
                                           allow_nan=False) + '\n' for case in expected)
        expected_response_hashes[state] = digest(response_text)
        sha(record['norms_sha256'], 'norm oracle hash')
        finite(record['oracle_s'], 'oracle time')
        require(record['query_seeds'] == ['zero', rows+200001, 17, rows+100001, rows+100002, rows+200005],
                'query seed definition changed')
        definitions[state] = record
    updates = records[3]
    require(updates['phase'] == 'update_definition' and updates['category_sql'] == "UPDATE docs SET category='c8'" and
            updates['front_id'] == 1 and updates['tail_id'] == rows and
            updates['front_seed'] == rows+100001 and updates['tail_seed'] == rows+100002,
            'mutation definition changed')
    for key in ('category', 'front', 'tail'):
        require(sha(updates[key+'_sql_sha256'], 'mutation SQL hash') == digest(updates[key+'_sql']),
                'mutation SQL hash mismatch')

    cursor = 4
    trials = {name: [] for name in names}
    response_hashes = {}
    device = None
    seen_trial_dirs = set()
    census_sql = "SELECT id,category,L2_DISTANCE(embedding,'["+','.join(['0']*dim)+"]') AS distance FROM docs ORDER BY id"
    census_sql_hash = digest(census_sql)
    load_requests = 1+(rows+batch-1)//batch
    for round_ in range(5):
        order = bins if round_ % 2 == 0 else bins[::-1]
        for position, binary in enumerate(order):
            name = Path(binary).name
            trial_dir = str(Path(config['work_dir']) / f'trial-{round_}-{name}')
            require(trial_dir not in seen_trial_dirs, 'database reused across trials')
            seen_trial_dirs.add(trial_dir)
            context = dict(name=name, binary=binary, binary_sha256=hashes[binary],
                           round=round_, position=position, trial_dir=trial_dir)

            def take(phase):
                nonlocal cursor
                require(cursor < len(records), f'missing {phase}')
                record = records[cursor]
                cursor += 1
                require(record.get('phase') == phase, f'raw execution order mismatch: expected {phase}')
                for key, expected in context.items():
                    require(record[key] == expected and type(record[key]) is type(expected), f'{phase}: wrong {key}')
                return record

            def response_digest(record, state):
                value = sha(record['response_sha256'], 'normalized response hash')
                if state in expected_response_hashes:
                    require(value == expected_response_hashes[state],
                            'TopK response hash differs from independent expected rows')
                require(response_hashes.setdefault(state, value) == value, 'response differs by version/round/restart')
                return value

            phases = {}
            request_id = 0
            process_pid = None

            def measured(phase):
                nonlocal process_pid, device
                record = take(phase)
                check_metrics(record)
                if phase in ('startup', 'recovery'):
                    require(record['startup_meter_started_before_exec'] is True and
                            record['startup_wall_s'] == record['t_s'], 'startup timing starts too late')
                    process_pid = record['pid']
                require(record['pid'] == process_pid, 'unexpected process change within lifecycle')
                if device is None:
                    device = record['device']
                require(record['device'] == device, 'physical I/O device changed')
                phases[phase] = record
                return record

            def queries(phase, state):
                nonlocal request_id
                record = measured(phase)
                ids = list(range(request_id+1, request_id+7))
                request_id += 6
                require(record['request_ids'] == ids and all(type(i) is int for i in record['request_ids']),
                        'TopK request IDs/order mismatch')
                require(record['reference_checked'] == 6 and record['reference_rows_checked'] == 60 and
                        record['sql_sha256'] == definitions[state]['sql_sha256'] and
                        record['truth_sha256'] == definitions[state]['truth_sha256'], 'TopK oracle/count mismatch')
                require(len(record['per_query_t_s']) == 6, 'missing per-query timing')
                for value in record['per_query_t_s']:
                    finite(value, 'query time')
                require(sum(record['per_query_t_s']) <= record['t_s']+1e-9, 'batch timing mismatch')
                response_digest(record, state)
                validation = take('query_validation')
                require(validation['batch'] == phase, 'query validation assigned to wrong batch')
                for key in ('request_ids', 'reference_checked', 'reference_rows_checked', 'sql_sha256',
                            'truth_sha256', 'response_sha256'):
                    require(validation[key] == record[key], 'query validation record mismatch')

            def census(phase):
                nonlocal request_id
                record = measured(phase)
                request_id += 1
                require(record['request_id'] == request_id and record['reference_checked'] == 1 and
                        record['reference_rows_checked'] == rows and record['sql_sha256'] == census_sql_hash,
                        'census ID/count/SQL mismatch')
                response_digest(record, 'census')
                validation = take('census_validation')
                require(validation['batch'] == phase and validation['scope'], 'census scope missing')
                for key in ('request_id', 'reference_checked', 'reference_rows_checked', 'response_sha256'):
                    require(validation[key] == record[key], 'census validation record mismatch')

            def shutdown(phase):
                nonlocal request_id
                record = measured(phase)
                request_id += 1
                require(record['process_exit_code'] == 0 and record['shutdown_request_id'] == request_id,
                        'database exit/acknowledgement mismatch')

            def evicted(stage, log_bytes):
                record = take('fixture_cache_evicted')
                require(record['stage'] == stage and record['path'] == str(Path(trial_dir)/'db/data.log') and
                        record['method'] == 'file_sync_then_posix_fadvise_dontneed' and record['file_bytes'] == log_bytes,
                        'cache eviction used wrong file or stage')
                resident = integer(record['resident_pages'], 'resident pages')
                page = integer(record['page_size'], 'page size', 1)
                require(resident <= 1 and record['total_pages'] == (log_bytes+page-1)//page,
                        'private file cache was not evicted after process exit')
                return resident

            require(take('trial_begin')['fresh_database'] is True, 'trial database is not fresh')
            measured('startup')
            loaded = measured('load')
            request_id = load_requests
            require(loaded['acknowledged_rows'] == rows and loaded['requests'] == load_requests and
                    loaded['last_request_id'] == request_id and loaded['generator_excluded_from_t'] is True and
                    loaded['includes_create_table'] is True, 'incomplete fresh load accounting')
            queries('index_query', 'initial')
            for phase, key, count in (('category_update', 'category', rows),
                                      ('point_update_front', 'front', 1), ('point_update_tail', 'tail', 1)):
                mutation = measured(phase)
                request_id += 1
                require(mutation['request_id'] == request_id and mutation['affected_rows_verified'] == count and
                        mutation['sql_sha256'] == updates[key+'_sql_sha256'] and mutation['index_prebuilt'] is True and
                        mutation['ack_includes_fdatasync'] is True, 'indexed update acknowledgement missing')
            queries('updated_query', 'updated')
            census('updated_census')
            shutdown('shutdown')
            snapshot = take('log_snapshot')
            log_bytes = integer(snapshot['log_bytes'], 'updated log bytes', rows*dim*4)
            log_hash = sha(snapshot['sha256'], 'committed log hash')
            require(snapshot['path'] == str(Path(trial_dir)/'db/data.log') and snapshot['state'] == 'updated_committed',
                    'wrong committed log snapshot')
            resident = evicted('before_recovery', log_bytes)
            request_id = 0
            recovered = measured('recovery')
            require(recovered['resident_pages_before_recovery'] == resident, 'recovery was not cold')
            queries('recovered_query', 'updated')
            census('recovered_census')
            shutdown('final_shutdown')
            unchanged = take('log_unchanged')
            require(unchanged['path'] == snapshot['path'] and unchanged['log_bytes'] == log_bytes and
                    unchanged['sha256'] == log_hash and unchanged['verified'] is True, 'recovery changed committed log')
            evicted('after_trial', log_bytes)
            completed = take('trial_complete')
            require(completed['topk_checked'] == 18 and completed['census_queries_checked'] == 2 and
                    completed['census_rows_checked'] == 2*rows and completed['affected_rows_verified'] == rows+2 and
                    completed['process_exits_verified'] == 2 and completed['log_sha256'] == log_hash and
                    completed['private_cache_evicted_after_stop'] is True, 'trial completion/counts invalid')
            for scope, included in (('indexed_update_ack_total', MUTATIONS), ('measured_stage_sum', STAGES)):
                phases[scope] = {'t_s': sum(phases[p]['t_s'] for p in included),
                                 'b_bytes': sum(phases[p]['b_bytes'] for p in included),
                                 'm_bytes': max(phases[p]['m_bytes'] for p in included),
                                 **{k: max(phases[p][k] for p in included) for k in
                                    ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')}}
            trials[name].append(dict(round=round_, position=position, phases=phases,
                                     log_sha256=log_hash, log_bytes=log_bytes))

    require(cursor == len(records)-1, 'extra/missing records after complete trials')
    final = records[cursor]
    require(final['completed_trials'] == 10 and final['topk_checked'] == 180 and
            final['census_queries_checked'] == 20 and final['census_rows_checked'] == 20*rows and
            final['affected_rows_verified'] == 10*(rows+2) and final['unique_state_query_oracles'] == 12 and
            final['process_exits_verified'] == 20, 'final truth/update/exit totals mismatch')
    require(final['binary_sha256_unchanged'] == hashes and final['response_sha256'] == response_hashes and
            final['cgroup_reused_across_restarts'] is True and final['all_private_caches_evicted_after_stop'] is True,
            'missing final identity/cache/accounting checks')
    return config, trials


def summarize(path, run_path):
    config, trials = validate(path, run_path)
    names = [Path(b).name for b in config['bins']]
    versions = []
    for name in names:
        scopes = {}
        for scope in SCOPES:
            runs = [dict(round=r['round'], position=r['position'],
                         **{k: r['phases'][scope][k] for k in METRICS},
                         **{k: r['phases'][scope][k] for k in
                            ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')}) for r in trials[name]]
            scopes[scope] = {'median': {k: statistics.median(r[k] for r in runs) for k in METRICS},
                             'runs': runs, 'sampled_maxima': {k: max(r[k] for r in runs) for k in
                              ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')}}
        versions.append({'name': name, 'rounds': 5, 'topk_response_checks': 90,
                         'census_query_checks': 10, 'census_rows_checked': 10*config['rows'],
                         'unique_state_query_oracles': 12, 'scopes': scopes,
                         'logs': [{k: r[k] for k in ('round', 'log_sha256', 'log_bytes')} for r in trials[name]]})
    paired = {}
    for scope in SCOPES:
        paired[scope] = []
        for round_ in range(5):
            b, c = [trials[name][round_]['phases'][scope] for name in names]
            paired[scope].append({'round': round_, **{k: c[k]/b[k] if b[k] else None for k in METRICS}})
    return {
        'validation_passed': True, 'complete_interleaving': True, 'rounds': 5,
        'file': path.name, 'run_metadata': run_path.name, 'workload': config,
        'reference_bad': 0, 'topk_response_checks_total': 180, 'unique_state_query_oracles': 12,
        'census_queries_checked': 20, 'census_rows_checked': 20*config['rows'],
        'versions': versions, 'paired_candidate_over_baseline': paired, 'no_weighted_E': True,
        'limitations': [
            'This is a small-data indexed-update experiment under an insufficient derived-index budget, not a >2GiB capacity check.',
            'The first TopK batch builds the implicit index; all three mutation ACK phases execute after that batch and include fdatasync and synchronous index maintenance.',
            'Category UPDATE changes every row from c9 to c8; point updates replace first/last vectors. Categories are not selective.',
            'The dataset/budget force full codes not to fit, but quantized-prefix coverage itself requires separate engine diagnostics.',
            'Initial and updated states have six independent exhaustive scalar TopK oracles each; replay after recovery reuses the updated truth and is counted as response checks, not new unique truth.',
            'Census checks every primary key, category and independent scalar norm; it does not compare every vector coordinate.',
            'Load preserves its own page cache for first-query/index construction and mutation phases; recovery begins after verified private-file eviction.',
            'The committed log hash is unchanged by restart; only private trial files are synced/evicted after each shutdown, and one cgroup is reused.',
            'measured_stage_sum adds recorded T/B and takes maximum M; it excludes generation, oracle, decoding, hashing and explicit eviction. No cross-stage weights or formal score are invented.',
            'All five rounds, including negative rounds, remain. Null paired ratios mean a zero reference metric.',
            'Sampled anon/file maxima are not simultaneous and cannot be added; M is the kernel peak reset/read using the same FD.',
            'No request deadline is enforced; separate full fresh-write/update/crash stress remains required.',
        ],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('path', type=Path)
    parser.add_argument('--run', type=Path, help='default: <input-stem>-run.json')
    args = parser.parse_args()
    run_path = args.run or args.path.with_name(args.path.stem+'-run.json')
    print(json.dumps(summarize(args.path, run_path), indent=2, ensure_ascii=False, allow_nan=False))


if __name__ == '__main__':
    main()
