"""Strict validation of large-fixture indexed-update comparisons; no weighted E."""
import argparse
import hashlib
import json
import math
from pathlib import Path
import re
import statistics


METRICS = ('t_s', 'b_bytes', 'm_bytes')
STAGES = ('startup', 'pre_update', 'category_update', 'post_update', 'census', 'shutdown')
SCOPES = STAGES + ('measured_stage_sum',)


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
    require(events.get('oom_group_kill', 0) == 0, 'cgroup OOM group kill')


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


def validate(path, run_path, allow_small=False):
    run = read_json(run_path)
    require(type(run['exit_code']) is int and run['exit_code'] == 0, 'driver did not exit successfully')
    argv = run['argv']
    require(isinstance(argv, list) and argv and all(isinstance(x, str) for x in argv), 'missing driver argv')
    before, after = run.get('driver_identities_before'), run.get('driver_identities_after')
    require(run.get('driver_identities_verified') is True and isinstance(before, dict) and
            isinstance(after, dict) and before == after, 'driver/source identity verification missing or failed')
    require(len(before) == 2 and argv[0] in before, 'driver identity map must contain binary and source')
    for artifact, value in before.items():
        require(isinstance(artifact, str) and artifact.startswith('/'), 'invalid driver identity path')
        sha(value, 'driver/source identity hash')
    source_paths = [artifact for artifact in before if artifact != argv[0]]
    require(source_paths[0].endswith('/performance/exact-tbm/round7/large_update/main.go'),
            'large-update source identity missing')
    records = [json.loads(line) for line in path.read_text(encoding='utf-8-sig').splitlines() if line.strip()]
    require(len(records) >= 6 and records[0].get('phase') == 'config' and records[-1].get('phase') == 'success',
            'missing config or final success')
    require(not any(r.get('phase') in ('failure', 'failed_phase', 'failed_request', 'process_read_failure') for r in records),
            'failed record present')
    counters = ('round', 'position', 'reference_bad', 'reference_checked', 'reference_rows_checked',
                'request_id', 'shutdown_request_id', 'process_exit_code', 'affected_rows_verified',
                'topk_checked', 'census_queries_checked', 'census_rows_checked', 'completed_trials',
                'unique_state_query_oracles', 'private_copies', 'verified_prefix_resets')
    truth_phases = {'pre_update', 'post_update', 'batch_validation', 'census', 'census_validation', 'trial_complete', 'success'}
    for record in records:
        for key in counters:
            if key in record:
                integer(record[key], key)
        if record.get('phase') in truth_phases:
            require(type(record.get('reference_bad')) is int and record['reference_bad'] == 0,
                    'truth record requires explicit integer-zero reference_bad')
        require(record.get('reference_bad', 0) == 0, 'truth failure')
        if 'memory_events' in record:
            check_events(record)
    config = records[0]
    require(config['mode'] == 'round7_large_update_interleaved' and type(config['rounds']) is int and config['rounds'] == 5,
            'wrong mode or incomplete five-round comparison')
    rows, dim = integer(config['rows'], 'rows', 100), integer(config['dim'], 'dim', 1)
    live = integer(config['live_rows'], 'live rows', 1)
    require(live == rows-3 and config['fixture_model'] == 'completed_final_all_c9', 'wrong final fixture model')
    require(config['raw_vector_bytes'] == rows*dim*4 and config['live_vector_bytes'] == live*dim*4,
            'raw/live vector byte count mismatch')
    exceeds = live*dim*4 > 2 << 30
    require(config['exceeds_2gib'] is exceeds and type(config['allow_small']) is bool, 'incorrect capacity metadata')
    require(exceeds or (allow_small and config['allow_small']), 'live vectors do not exceed 2GiB')
    require(config['queries_per_batch'] == 6 and config['top_k'] == 10 and config['unique_state_query_oracles'] == 12,
            'unexpected query count/K')
    require(config['memory_limit'] == 2 << 30 and config['memory_swap_max'] == 0 and
            config['cpu_max'] == '200000 100000' and config['pids_max'] == 128 and config['index_mem_mb'] == 256 and
            config['sync'] == 'fdatasync' and config['search_mode'] == 'exact', 'limits, index budget or durability changed')
    require(config['database_env'] == ['GOMAXPROCS=2', 'GOMEMLIMIT=1288490188', 'GOGC=20'] and
            config['inherited_mvdb_env_removed'] is True, 'runtime environment changed')
    bins = config['bins']
    require(len(bins) == 2 and all(isinstance(b, str) and b.startswith('/') for b in bins), 'invalid binary list')
    names = [Path(b).name for b in bins]
    require(len(set(names)) == 2, 'duplicate binary basenames')
    hashes = config['binary_sha256']
    require(set(hashes) == set(bins), 'missing database binary identities')
    for value in hashes.values():
        sha(value, 'database binary hash')
    for key, expected, default in (
        ('bins', ','.join(bins), None), ('fixture', config['fixture_source'], None),
        ('work-dir', config['work_dir'], None), ('rows', str(rows), '550000'), ('dim', str(dim), '1024'),
    ):
        require(option(argv, key, default) == expected, f'argv/config mismatch for {key}')

    definitions, expected_response_hashes = {}, {}
    deleted = {rows//3, rows//2, rows-1}
    for record, state, category in zip(records[1:3], ('initial', 'updated'), ('c9', 'c8')):
        require(record['phase'] == 'oracle_definition' and record['state'] == state and record['category'] == category,
                'missing initial/updated independent oracle')
        require(record['unique_queries'] == 6 and record['oracle_rows_per_query'] == live and
                record['vector_oracle_reused_across_category_states'] is True, 'oracle scope/count mismatch')
        statements, labels = record['statements'], record['labels']
        require(len(statements) == len(set(statements)) == len(labels) == len(set(labels)) == 6,
                'missing or duplicated SQL cases')
        require(sha(record['sql_sha256'], 'SQL hash') == digest('\n'.join(statements)), 'SQL hash mismatch')
        expected = record['expected_topk']
        require(len(expected) == 6, 'oracle case count mismatch')
        for case in expected:
            require(len(case) == 10, 'oracle K mismatch')
            previous, seen = None, set()
            for row in case:
                require(len(row) == 3, 'oracle row width mismatch')
                pk, distance = integer(row[0], 'oracle PK', 1), finite(row[2], 'oracle distance')
                require(pk <= rows and pk not in deleted and pk not in seen and row[1] == category, 'invalid oracle row')
                pair = distance, pk
                require(previous is None or pair > previous, 'oracle tie order mismatch')
                previous = pair
                seen.add(pk)
        encode = lambda value: json.dumps(value, separators=(',', ':'), ensure_ascii=False, allow_nan=False)
        require(sha(record['truth_sha256'], 'truth hash') == digest(encode(expected)), 'oracle definition hash mismatch')
        expected_hash = digest(''.join(encode(case)+'\n' for case in expected))
        require(sha(record['expected_response_sha256'], 'oracle response hash') == expected_hash, 'oracle response hash binding failed')
        expected_response_hashes[state] = expected_hash
        require(record['query_seeds'] == ['zero', rows+700001, 17, rows+100001, 1000018, rows+200005],
                'fixture/query seed model changed')
        finite(record['oracle_s'], 'oracle time')
        definitions[state] = record
    # Vectors and live IDs are unchanged by category UPDATE. Check this
    # relationship in addition to validating each state's category separately.
    for initial, updated in zip(definitions['initial']['expected_topk'], definitions['updated']['expected_topk']):
        require([(r[0], r[2]) for r in initial] == [(r[0], r[2]) for r in updated], 'category-only update changed vector truth')
    census_hash = sha(definitions['updated']['census_expected_response_sha256'], 'independent census oracle hash')
    updates = records[3]
    require(updates['phase'] == 'update_definition' and updates['sql'] == "UPDATE docs SET category='c8'" and
            updates['sql_sha256'] == digest(updates['sql']) and updates['affected_rows'] == live and
            updates['from_category'] == 'c9' and updates['to_category'] == 'c8', 'mutation definition changed')
    fixture = records[4]
    require(fixture['phase'] == 'fixture_ready' and fixture['private_copies'] == 1 and
            fixture['distinct_inode_verified'] is True, 'private fixture identity/preparation incomplete')
    source_hash = sha(fixture['source_sha256'], 'source hash')
    require(fixture['private_sha256'] == source_hash, 'private copy differs from source')
    original_bytes = integer(fixture['fixture_bytes'], 'original log bytes', rows*dim*4)
    private_path = str(Path(config['work_dir'])/'db/data.log')
    require(fixture['private_log'] == private_path and private_path != config['fixture_source'], 'private log path invalid')
    require(isinstance(fixture['source_signature'], str) and re.fullmatch(r'\d+:\d+', fixture['source_signature']) and
            int(fixture['source_signature'].split(':')[0]) == original_bytes, 'source metadata signature invalid')

    cursor, device = 5, None
    trials = {name: [] for name in names}
    census_sql = "SELECT id,category,L2_DISTANCE(embedding,'["+','.join(['0']*dim)+"]') AS distance FROM docs ORDER BY id"
    for round_ in range(5):
        order = bins if round_ % 2 == 0 else bins[::-1]
        for position, binary in enumerate(order):
            name = Path(binary).name
            context = dict(name=name, binary=binary, binary_sha256=hashes[binary], round=round_, position=position)

            def take(phase):
                nonlocal cursor
                require(cursor < len(records), f'missing {phase}')
                record = records[cursor]
                cursor += 1
                require(record.get('phase') == phase, f'raw order mismatch: expected {phase}, got {record.get("phase")}')
                for key, expected in context.items():
                    require(record[key] == expected and type(record[key]) is type(expected), f'{phase}: incorrect {key}')
                return record

            def cache(stage):
                record = take('fixture_cache_evicted')
                require(record['stage'] == stage and record['path'] == private_path and record['file_bytes'] == original_bytes and
                        record['method'] == 'file_sync_then_posix_fadvise_dontneed', 'cache eviction scope changed')
                page = integer(record['page_size'], 'page size', 1)
                resident = integer(record['resident_pages'], 'resident pages')
                require(resident <= 1 and record['total_pages'] == (original_bytes+page-1)//page, 'private fixture was not cold')
                return resident

            phases = {}
            pid = None

            def measured(phase):
                nonlocal pid, device
                record = take(phase)
                check_metrics(record)
                if pid is None:
                    pid = record['pid']
                require(record['pid'] == pid, 'database process changed within trial')
                if device is None:
                    device = record['device']
                require(record['device'] == device, 'data device changed')
                phases[phase] = record
                return record

            def queries(phase, state, ids):
                record = measured(phase)
                require(record['batch'] == phase and record['state'] == state and record['request_ids'] == ids and
                        all(type(i) is int for i in record['request_ids']), 'TopK phase/state/request sequence mismatch')
                require(record['reference_checked'] == 6 and record['reference_rows_checked'] == 60 and
                        record['sql_sha256'] == definitions[state]['sql_sha256'] and
                        record['truth_sha256'] == definitions[state]['truth_sha256'] and
                        record['response_sha256'] == expected_response_hashes[state], 'TopK truth/hash/count mismatch')
                require(len(record['per_query_t_s']) == 6, 'missing per-query timing')
                for value in record['per_query_t_s']:
                    finite(value, 'query time')
                require(sum(record['per_query_t_s']) <= record['t_s']+1e-9, 'query batch time excludes work')
                validation = take('batch_validation')
                for key in ('batch', 'state', 'request_ids', 'reference_checked', 'reference_rows_checked',
                            'sql_sha256', 'truth_sha256', 'response_sha256'):
                    require(validation[key] == record[key], 'TopK validation record mismatch')
                return record

            begun = take('trial_begin')
            require(begun['private_inode_verified'] is True and begun['initial_log_bytes'] == original_bytes, 'trial did not restore initial private state')
            resident = cache('before_startup')
            startup = measured('startup')
            require(startup['startup_meter_started_before_exec'] is True and startup['startup_wall_s'] == startup['t_s'] and
                    startup['resident_pages_before_startup'] == resident, 'startup cold/timing evidence missing')
            resident = cache('before_pre_update')
            pre = queries('pre_update', 'initial', list(range(1, 7)))
            require(pre['cache_state'] == 'cold_at_batch_start' and pre['resident_pages_before_batch'] == resident, 'pre-update batch not initially cold')
            update = measured('category_update')
            require(update['request_id'] == 7 and update['affected_rows_verified'] == live and
                    update['sql_sha256'] == updates['sql_sha256'] and update['index_prebuilt'] is True and
                    update['ack_includes_fdatasync'] is True, 'indexed UPDATE confirmation incomplete')
            post = queries('post_update', 'updated', list(range(8, 14)))
            require(post['cache_state'] == 'same_process_after_update_no_eviction', 'post-update cache policy changed')
            census = measured('census')
            require(census['request_id'] == 14 and census['reference_checked'] == 1 and census['reference_rows_checked'] == live and
                    census['sql_sha256'] == digest(census_sql) and census['response_sha256'] == census_hash, 'census independent truth/count/hash mismatch')
            validation = take('census_validation')
            for key in ('request_id', 'reference_checked', 'reference_rows_checked', 'response_sha256'):
                require(validation[key] == census[key], 'census validation record mismatch')
            require(validation['scope'], 'missing census scope')
            shutdown = measured('shutdown')
            require(shutdown['process_exit_code'] == 0 and shutdown['shutdown_request_id'] == 15, 'database did not exit cleanly')
            prefix = take('prefix_verified')
            for key in ('prefix_bytes', 'original_bytes', 'appended_log_bytes', 'appended_bytes'):
                integer(prefix[key], key)
            require(prefix['path'] == private_path and prefix['prefix_bytes'] == prefix['original_bytes'] == original_bytes and
                    prefix['prefix_sha256'] == source_hash and prefix['appended_bytes'] >= live*dim*4 and
                    prefix['appended_log_bytes'] == original_bytes+prefix['appended_bytes'] and
                    prefix['private_inode_verified'] is True and prefix['source_inode_distinct'] is True and
                    prefix['process_exited_before_reset'] is True, 'unsafe/incomplete original-prefix verification')
            restored = take('fixture_restored')
            require(restored['path'] == private_path and restored['file_bytes'] == original_bytes and restored['sha256'] == source_hash and
                    restored['truncate_synced'] is True and restored['private_inode_verified'] is True and
                    restored['source_inode_distinct'] is True, 'private truncate/sync/full-hash reset incomplete')
            cache('after_restore')
            completed = take('trial_complete')
            require(completed['topk_checked'] == 12 and completed['census_queries_checked'] == 1 and completed['census_rows_checked'] == live and
                    completed['affected_rows_verified'] == live and completed['process_exit_code'] == 0 and
                    completed['private_restored'] is True and completed['source_metadata_unchanged'] is True, 'trial totals/reset missing')
            phases['measured_stage_sum'] = {'t_s': sum(phases[p]['t_s'] for p in STAGES),
                                            'b_bytes': sum(phases[p]['b_bytes'] for p in STAGES),
                                            'm_bytes': max(phases[p]['m_bytes'] for p in STAGES),
                                            **{k: max(phases[p][k] for p in STAGES) for k in
                                               ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')}}
            trials[name].append(dict(round=round_, position=position, phases=phases,
                                     appended_log_bytes=prefix['appended_log_bytes'], appended_bytes=prefix['appended_bytes']))
    require(cursor == len(records)-1, 'extra or missing records after completed trials')
    final = records[cursor]
    require(final['completed_trials'] == 10 and final['topk_checked'] == 120 and final['census_queries_checked'] == 10 and
            final['census_rows_checked'] == 10*live and final['affected_rows_verified'] == 10*live and
            final['unique_state_query_oracles'] == 12 and final['private_copies'] == 1 and final['verified_prefix_resets'] == 10,
            'final truth/update/reset totals mismatch')
    require(final['source_sha256_unchanged'] == final['private_sha256_restored'] == source_hash and
            final['source_metadata_unchanged'] is True and final['binary_sha256_unchanged'] == hashes and
            final['cgroup_reused_across_restarts'] is True, 'final source/private/binary identity proof missing')
    return config, trials, source_hash


def summarize(path, run_path, allow_small=False):
    config, trials, fixture_hash = validate(path, run_path, allow_small)
    names = [Path(b).name for b in config['bins']]
    versions = []
    for name in names:
        scopes = {}
        for scope in SCOPES:
            runs = [dict(round=r['round'], position=r['position'], **{k: r['phases'][scope][k] for k in METRICS},
                         **{k: r['phases'][scope][k] for k in ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')})
                    for r in trials[name]]
            scopes[scope] = {'median': {k: statistics.median(r[k] for r in runs) for k in METRICS}, 'runs': runs,
                             'sampled_maxima': {k: max(r[k] for r in runs) for k in
                                                ('max_sampled_anon', 'max_sampled_file', 'max_sampled_rss')}}
        versions.append({'name': name, 'rounds': 5, 'topk_response_checks': 60, 'census_query_checks': 5,
                         'census_rows_checked': 5*config['live_rows'], 'unique_state_query_oracles': 12, 'scopes': scopes,
                         'reset_records': [{k: r[k] for k in ('round', 'appended_log_bytes', 'appended_bytes')} for r in trials[name]]})
    paired = {}
    for scope in SCOPES:
        paired[scope] = []
        for round_ in range(5):
            b, c = [trials[name][round_]['phases'][scope] for name in names]
            paired[scope].append({'round': round_, **{k: c[k]/b[k] if b[k] else None for k in METRICS}})
    return {'validation_passed': True, 'complete_interleaving': True, 'rounds': 5, 'file': path.name,
            'run_metadata': run_path.name, 'workload': config, 'fixture_sha256': fixture_hash, 'reference_bad': 0,
            'topk_response_checks_total': 120, 'unique_state_query_oracles': 12, 'census_queries_checked': 10,
            'census_rows_checked': 10*config['live_rows'], 'versions': versions, 'paired_candidate_over_baseline': paired,
            'no_weighted_E': True, 'verified_prefix_resets': 10,
            'limitations': [
                'One original final-all-c9 fixture is copied once to a distinct private inode; only that inode is reset after verified process exit and original-prefix SHA256.',
                'All live vectors exceed 2GiB unless explicit allow-small smoke mode is used. The update appends another full live-table image.',
                'Pre-update batch starts with cold file pages and includes first-query implicit-index construction; only the batch start is guaranteed cold.',
                'UPDATE ACK includes fdatasync and synchronous index maintenance. Post-update queries and full census retain the same process/cache and are measured separately.',
                'Initial and updated state oracles differ only in category; one exhaustive independent scalar distance calculation is reused because no vector changes.',
                'TopK hashes bind to the canonical independent expected result; every census row is checked against independent PK/category/norm truth, not elementwise vector contents.',
                'Original prefix hash and private inode are checked before truncation; restored full hash, explicit Sync and mincore cold pages are checked before the next trial.',
                'measured_stage_sum adds startup+pre_update+category_update+post_update+census+shutdown T/B and takes maximum M; reset/hash/eviction and external validation are excluded.',
                'No phase weights, weighted E or platform score are invented. All five paired rounds remain, including negative rounds; null ratios have zero reference metrics.',
                'Sampled anon/file maxima may occur at different times and cannot be added. M is the same-FD reset/read kernel cgroup peak.',
                'This comparison does not restart the updated database; final fresh-write/update/crash stress supplies recovery evidence separately.',
                'No per-request deadline is enforced; completed local runs do not establish a formal time-limit pass.',
            ]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('path', type=Path)
    parser.add_argument('--run', type=Path, help='default: <input-stem>-run.json')
    parser.add_argument('--allow-small', action='store_true')
    args = parser.parse_args()
    run_path = args.run or args.path.with_name(args.path.stem+'-run.json')
    print(json.dumps(summarize(args.path, run_path, args.allow_small), indent=2, ensure_ascii=False, allow_nan=False))


if __name__ == '__main__':
    main()
