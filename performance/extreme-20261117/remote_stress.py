"""Server capacity validation on Linux 6.5: cumulative memory.peak only.

This is a capacity/correctness check, with no comparison E or score estimate.
Every output and the data directory must be new; an interrupted attempt cannot
be silently replaced by rerunning the same name.
"""
import argparse
from contextlib import ExitStack
import hashlib
import json
import math
from pathlib import Path
import re
import subprocess
import time

from lab import ENV, GO, REPO, ROOT, OUT as RESULTS
import fcntl
import os


ROWS, DIM, QUERIES, BATCH = 550000, 1024, 12, 64
MEMORY_LIMIT = 2 << 30
RAW_BYTES = ROWS * DIM * 4
PHASES = [
    'initial_startup', 'write', 'query_after_write', 'updates_deletes_rollback',
    'query_after_updates', 'kill_after_acknowledged_commits',
    'recovery_after_commit_kill', 'query_after_commit_recovery',
    'uncommitted_tail_flushed', 'kill_with_uncommitted_transaction',
    'kill_during_recovery', 'final_recovery', 'query_after_final_recovery',
    'final_full_census', 'bulk_update', 'query_after_bulk_update',
    'kill_after_bulk_commit', 'recovery_after_bulk_commit',
    'query_after_bulk_recovery', 'final_full_census', 'graceful_shutdown',
]
KILLS = ['kill_after_acknowledged_commits', 'kill_with_uncommitted_transaction',
         'kill_during_recovery', 'kill_after_bulk_commit']


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def write_json(stream, obj):
    json.dump(obj, stream, indent=2)
    stream.write('\n')
    stream.flush()


def validate(records, binary, data_dir):
    require(records and records[0].get('phase') == 'config', 'missing config')
    require(records[-1].get('phase') == 'success', 'missing final success')
    require(not any(r.get('phase') in ('failure', 'failed_phase', 'failed_request',
                                      'process_read_failure') for r in records),
            'driver reported failure')
    config, final = records[0], records[-1]
    for key, expected in {
        'binary': str(binary), 'data_dir': str(data_dir), 'rows': ROWS, 'dim': DIM,
        'queries_per_state': QUERIES, 'batch': BATCH, 'raw_vector_bytes': RAW_BYTES,
        'exceeds_2gib': True, 'bulk_update': True, 'memory_limit': MEMORY_LIMIT,
        'memory_swap_max': 0, 'cpu_max': '200000 100000', 'index_mem_mb': 256,
        'durability': 'explicit fdatasync',
    }.items():
        require(config.get(key) == expected, f'config {key} mismatch')
    require(RAW_BYTES > MEMORY_LIMIT, 'fixture does not exceed memory limit')
    require(config.get('env') == ['GOMAXPROCS=2', 'GOMEMLIMIT=1288490188', 'GOGC=20'],
            'unexpected database runtime environment')

    measured = [r for r in records if 't_s' in r]
    require([r.get('phase') for r in measured] == PHASES,
            'missing, duplicated or reordered workflow phases')
    for record in records:
        require(record.get('reference_bad', 0) == 0, 'independent truth mismatch')
        if 'memory_events' in record:
            events = record['memory_events']
            require('oom' in events and 'oom_kill' in events,
                    'missing cgroup OOM counters')
            require(events['oom'] == 0 and events['oom_kill'] == 0,
                    'cgroup OOM or OOM kill')
    for record in measured:
        phase = record['phase']
        require(record.get('memory_peak_scope') == 'whole_workflow',
                f'{phase}: expected cumulative peak on the probed server kernel')
        require('memory_events' in record, f'{phase}: missing cgroup counters')
        require(record.get('sample_period_ms') == 50, f'{phase}: sampling changed')
        for key in ('t_s', 'wall_s', 'm_bytes', 'b_bytes', 'r_bytes', 'w_bytes'):
            value = record.get(key)
            require(isinstance(value, (int, float)) and math.isfinite(value) and value >= 0,
                    f'{phase}: invalid {key}')
        require(record['b_bytes'] == record['r_bytes'] + record['w_bytes'],
                f'{phase}: I/O total mismatch')
        require(record['m_bytes'] > 0, f'{phase}: missing memory peak')
        if phase == 'initial_startup' or 'recovery' in phase and not phase.startswith('query_'):
            require(record.get('startup_meter_started_before_exec') is True,
                    f'{phase}: startup measurement begins too late')

    peaks = [r['m_bytes'] for r in measured]
    require(peaks == sorted(peaks), 'cumulative peak decreased')

    def one(phase):
        matches = [r for r in records if r.get('phase') == phase]
        require(len(matches) == 1, f'expected one {phase}, got {len(matches)}')
        return matches[0]

    written = one('write')
    require(written.get('rows_acknowledged') == ROWS and
            written.get('raw_vector_bytes') == RAW_BYTES and written['log_bytes'] > RAW_BYTES,
            'incomplete large write')
    require(written.get('generator_excluded_from_t') is True,
            'write timing includes data generation')
    query_phases = [r for r in measured if r['phase'].startswith('query_')]
    require(len(query_phases) == 6, 'missing independently checked query state')
    for record in query_phases:
        require(record.get('reference_checked') == QUERIES and record.get('reference_bad') == 0,
                f'{record["phase"]}: incomplete TopK truth')
        require(math.isfinite(record.get('oracle_s', -1)) and record['oracle_s'] >= 0,
                f'{record["phase"]}: missing oracle timing')

    flushed, discarded = one('uncommitted_tail_flushed'), one('uncommitted_tail_discarded')
    tail = flushed.get('uncommitted_tail_bytes', 0)
    require(tail > 0 and flushed['log_bytes'] == flushed['committed_log_bytes'] + tail,
            'uncommitted transaction never reached the log file')
    require(flushed.get('transaction_committed') is False and
            flushed.get('reference_checked') == 1 and flushed.get('point_rows_checked') == 3 and
            flushed.get('reference_bad') == 0, 'missing transaction visibility truth')
    require(discarded.get('committed_log_bytes') == flushed['committed_log_bytes'] and
            discarded.get('recovered_log_bytes') == flushed['committed_log_bytes'] and
            discarded.get('discarded_tail_bytes') == tail and
            discarded.get('point_rows_checked') == 3 and discarded.get('reference_bad') == 0,
            'uncommitted tail recovery was not verified')
    for phase in KILLS:
        require(one(phase).get('signal') == 'SIGKILL', f'{phase}: missing SIGKILL')
    during = one('kill_during_recovery')
    require(during.get('killed_before_ready') is True and during.get('delay_ms') == 10,
            'recovery interruption missed the pre-ready window')

    censuses = [r for r in measured if r['phase'] == 'final_full_census']
    require(len(censuses) == 2 and all(r.get('reference_rows_checked') == ROWS - 3 and
                                     r.get('reference_bad') == 0 for r in censuses),
            'incomplete census before/after bulk update')
    require(one('bulk_update').get('affected_rows') == ROWS - 3,
            'bulk update affected-row count mismatch')
    basic = one('basic_workflow_success')
    require(basic.get('reference_checked') == 4 * QUERIES + 1 and
            basic.get('full_census_checked') is True, 'incomplete basic workflow')
    for record in (basic, final):
        require(record.get('cgroup_reused_across_restarts') is True and
                record.get('reference_bad') == 0 and 'memory_events' in record,
                'missing restart accounting or truth')
    require(final.get('reference_checked') == 6 * QUERIES + 1 ==
            sum(r.get('reference_checked', 0) for r in measured), 'TopK totals mismatch')
    require(final.get('full_census_checked') is True and final.get('exceeds_2gib') is True and
            final.get('raw_vector_bytes') == RAW_BYTES, 'incomplete final capacity check')
    require(final.get('data_dir') == str(data_dir), 'final data directory mismatch')
    log = data_dir / 'db/data.log'
    require(final.get('log_bytes') == log.stat().st_size and final['log_bytes'] > RAW_BYTES,
            'final log size mismatch')
    peak = final.get('whole_workflow_m_bytes', 0)
    require(peak >= max(r['m_bytes'] for r in measured), 'whole-workflow peak below phase peak')
    return {
        'validation_passed': True, 'single_version_capacity_check': True,
        'no_comparison_E': True, 'config': config, 'success': final,
        'independent_topk_queries': 6 * QUERIES + 1,
        'census_rows_checked': sum(r['reference_rows_checked'] for r in censuses),
        'transaction_point_rows_checked': 6, 'sigkill_events': len(KILLS),
        'uncommitted_tail_bytes': tail, 'per_phase_peak_reset_verified': False,
        'memory_peak_scope': 'whole_workflow',
        'cumulative_memory_peaks_monotonic': True,
        'memory_limit_bytes': MEMORY_LIMIT, 'whole_workflow_peak_bytes': peak,
        'peak_above_configured_limit_bytes': max(0, peak - MEMORY_LIMIT),
        'phases': measured,
        'limits': [
            'census checks primary keys, categories and independently regenerated norms; '
            'it does not project and compare every vector coordinate',
            'pre-ready SIGKILL is verified; there is no internal recovery progress hook',
            'full workflow has no per-request deadline and is not a formal time-limit pass',
            'kernel 6.5 cannot reset memory.peak; phase M is cumulative since workflow start',
            'memory.peak includes file cache; sampled anon/file maxima are not additive',
            'phase T excludes external oracle generation; no phase weights are invented',
        ],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('name', help='already built candidate name, e.g. final')
    parser.add_argument('--stress-label', help='New run label for outputs and data directory; defaults to NAME-stress')
    args = parser.parse_args()
    require(re.fullmatch(r'[A-Za-z0-9_-]+', args.name), 'invalid candidate name')
    require(args.stress_label is None or re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]*', args.stress_label),
            'invalid stress label')
    name = args.name
    binary = (ROOT / 'bin' / name).resolve()
    require(binary.is_file(), f'missing candidate binary: {binary}')
    require(RESULTS.is_dir(), f'missing results directory: {RESULTS}')
    source = REPO / 'performance/extreme-20261117/remote_stress.go'
    label = args.stress_label or name + '-stress'
    data_dir = (ROOT / label).resolve()
    data_dir.mkdir()  # Never reuse an old or partially completed database.
    driver = data_dir / 'stress-driver'
    build_argv = [str(GO), 'build', '-trimpath', '-o', str(driver), str(source)]
    argv = [str(driver), '-binary', str(binary), '-data-dir', str(data_dir),
            '-rows', str(ROWS), '-dim', str(DIM), '-queries', str(QUERIES),
            '-batch', str(BATCH), '-bulk-update', '-recovery-kill-delay', '10ms']
    metadata = {'candidate': name, 'stress_label': label, 'candidate_binary': str(binary),
                'candidate_sha256': digest(binary), 'driver_source': str(source),
                'driver_source_sha256': digest(source), 'build_argv': build_argv,
                'argv': argv, 'data_dir': str(data_dir), 'exit_code': None,
                'validation_passed': False, 'official_submissions': 0,
                'database_env': ['GOMAXPROCS=2', 'GOMEMLIMIT=1288490188', 'GOGC=20']}
    with ExitStack() as stack:
        # Claim all artifacts before building or starting the workload.
        handles = {key: stack.enter_context((RESULTS / (label + suffix)).open('x'))
                   for key, suffix in {
                       'build': '-build.txt', 'out': '.jsonl', 'err': '.stderr.txt',
                       'meta': '-run.json', 'summary': '-summary.json',
                   }.items()}
        start = time.time()
        try:
            print(label + ': build', flush=True)
            write_json(handles['build'], {'argv': build_argv, 'start': start})
            built = subprocess.run(build_argv, env=ENV, stdout=handles['build'],
                                   stderr=subprocess.STDOUT)
            metadata['build_exit_code'] = built.returncode
            built.check_returncode()
            metadata['driver_sha256'] = digest(driver)
            metadata['start'] = time.time()
            print(label + ': full workflow start', flush=True)
            process = subprocess.run(argv, env=ENV, stdout=handles['out'], stderr=handles['err'])
            metadata['exit_code'] = process.returncode
            metadata['workflow_wall_s'] = time.time() - metadata['start']
            process.check_returncode()
            handles['out'].flush()
            records = [json.loads(line) for line in (RESULTS / (label + '.jsonl')).read_text().splitlines()
                       if line.strip()]
            require(digest(binary) == metadata['candidate_sha256'], 'candidate changed during workflow')
            require(digest(source) == metadata['driver_source_sha256'], 'driver source changed during workflow')
            summary = validate(records, binary, data_dir)
            metadata['validation_passed'] = True
            write_json(handles['summary'], summary)
            print(label + ': PASS', flush=True)
        except BaseException as problem:
            metadata['error'] = repr(problem)
            write_json(handles['summary'], {'validation_passed': False, 'error': repr(problem),
                                            'single_version_capacity_check': True})
            raise
        finally:
            metadata['build_and_workflow_wall_s'] = time.time() - start
            write_json(handles['meta'], metadata)


if __name__ == '__main__':
    require((ROOT.parent / 'MIGRATION_COMPLETE.json').is_file(), 'migration incomplete')
    require(os.getuid() == 0, 'use sudo -n')
    with (ROOT.parent / 'remote-experiment.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        main()
