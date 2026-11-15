"""Reject damaged copies of a completed small large-update driver run."""
import argparse
import copy
import json
from pathlib import Path

from analyze import summarize


parser = argparse.ArgumentParser()
parser.add_argument('input', type=Path)
parser.add_argument('output_dir', type=Path)
parser.add_argument('summary', type=Path)
args = parser.parse_args()
args.output_dir.mkdir()
records = [json.loads(line) for line in args.input.read_text().splitlines()]
metadata = json.loads(args.input.with_name(args.input.stem+'-run.json').read_text())


def first(items, phase):
    return next(record for record in items if record['phase'] == phase)


def zero_writes(items, run):
    record = first(items, 'category_update')
    record.update(w_bytes=0, b_bytes=record['r_bytes'])


def reorder_reset(items, run):
    a = items.index(first(items, 'prefix_verified'))
    b = items.index(first(items, 'fixture_restored'))
    items[a], items[b] = items[b], items[a]


def check(name, mutate=None):
    items, run = copy.deepcopy(records), copy.deepcopy(metadata)
    if mutate:
        mutate(items, run)
    path = args.output_dir/(name+'.jsonl')
    path.write_text(''.join(json.dumps(record)+'\n' for record in items))
    run_path = args.output_dir/(name+'-run.json')
    run_path.write_text(json.dumps(run)+'\n')
    try:
        summarize(path, run_path, True)
    except (ValueError, KeyError, AssertionError) as problem:
        if not mutate:
            raise
        return {'case': name, 'rejected': True, 'reason': str(problem)}
    if mutate:
        raise AssertionError('Damaged record was accepted: '+name)
    return {'case': name, 'valid_copy_passed': True}


cases = [check('valid-copy')]
for name, mutate in [
    ('wrong-position', lambda rs, m: first(rs, 'trial_begin').update(position=1)),
    ('missing-update', lambda rs, m: rs.remove(first(rs, 'category_update'))),
    ('wrong-affected', lambda rs, m: first(rs, 'category_update').update(affected_rows_verified=1)),
    ('wrong-census', lambda rs, m: first(rs, 'census').update(reference_rows_checked=1)),
    ('wrong-request', lambda rs, m: first(rs, 'category_update').update(request_id=999999)),
    ('false-peak', lambda rs, m: first(rs, 'category_update').update(memory_peak_scope='not_reset')),
    ('zero-durable-writes', zero_writes),
    ('wrong-growth', lambda rs, m: first(rs, 'category_update').update(log_growth_bytes=0)),
    ('false-write-verification', lambda rs, m: first(rs, 'category_update').update(durable_write_accounting_verified=False)),
    ('false-producer-cgroup', lambda rs, m: first(rs, 'fixture_ready').update(producer_cgroup_verified=False)),
    ('wrong-producer-cgroup', lambda rs, m: first(rs, 'fixture_ready').update(producer_cgroup='/sys/fs/cgroup/other')),
    ('wrong-reset-cgroup', lambda rs, m: first(rs, 'fixture_restored').update(reset_cgroup='/sys/fs/cgroup/other')),
    ('wrong-prefix-hash', lambda rs, m: first(rs, 'prefix_verified').update(prefix_sha256='0'*64)),
    ('wrong-restored-hash', lambda rs, m: first(rs, 'fixture_restored').update(sha256='0'*64)),
    ('unsynced-reset', lambda rs, m: first(rs, 'fixture_restored').update(truncate_synced=False)),
    ('reset-before-prefix', reorder_reset),
    ('missing-prefix', lambda rs, m: rs.remove(first(rs, 'prefix_verified'))),
    ('false-private-inode', lambda rs, m: first(rs, 'prefix_verified').update(private_inode_verified=False)),
    ('warm-pages', lambda rs, m: first(rs, 'fixture_cache_evicted').update(resident_pages=2)),
    ('oom', lambda rs, m: first(rs, 'category_update')['memory_events'].update(oom=1)),
    ('oom-group', lambda rs, m: first(rs, 'category_update')['memory_events'].update(oom_group_kill=1)),
    ('nonzero-process-exit', lambda rs, m: first(rs, 'shutdown').update(process_exit_code=1)),
    ('nonzero-driver-exit', lambda rs, m: m.update(exit_code=1)),
    ('missing-driver-verification', lambda rs, m: m.pop('driver_identities_verified')),
    ('changed-driver', lambda rs, m: m['driver_identities_after'].update({m['argv'][0]: '0'*64})),
    ('missing-truth-flag', lambda rs, m: first(rs, 'batch_validation').pop('reference_bad')),
    ('wrong-topk-hash', lambda rs, m: first(rs, 'pre_update').update(response_sha256='0'*64)),
    ('missing-success', lambda rs, m: rs.pop()),
]:
    cases.append(check(name, mutate))
with args.summary.open('x') as output:
    json.dump({'validator_selfcheck_passed': True, 'source_smoke': str(args.input),
               'not_database_performance_evidence': True, 'cases': cases}, output, indent=2)
    output.write('\n')
print('PASS: valid smoke accepted; all damaged copies rejected')
