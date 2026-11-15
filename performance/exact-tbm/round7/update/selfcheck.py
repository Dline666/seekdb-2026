"""Check that the validator rejects damaged copies of one completed smoke run."""
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


def forge_consistent_topk(items, run):
    for record in items:
        if record['phase'] in {'index_query', 'updated_query', 'recovered_query', 'query_validation'}:
            record['response_sha256'] = '1'*64
        if record['phase'] == 'success':
            record['response_sha256'].update(initial='1'*64, updated='1'*64)


def check(name, mutate=None):
    items, run = copy.deepcopy(records), copy.deepcopy(metadata)
    if mutate:
        mutate(items, run)
    path = args.output_dir/(name+'.jsonl')
    path.write_text(''.join(json.dumps(record)+'\n' for record in items))
    run_path = args.output_dir/(name+'-run.json')
    run_path.write_text(json.dumps(run)+'\n')
    try:
        summarize(path, run_path)
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
    ('wrong-census', lambda rs, m: first(rs, 'updated_census').update(reference_rows_checked=1)),
    ('wrong-request', lambda rs, m: first(rs, 'point_update_front').update(request_id=999999)),
    ('false-peak', lambda rs, m: first(rs, 'load').update(memory_peak_scope='not_reset')),
    ('wrong-log-hash', lambda rs, m: first(rs, 'log_unchanged').update(sha256='0'*64)),
    ('oom', lambda rs, m: first(rs, 'category_update')['memory_events'].update(oom=1)),
    ('nonzero-process-exit', lambda rs, m: first(rs, 'shutdown').update(process_exit_code=1)),
    ('nonzero-driver-exit', lambda rs, m: m.update(exit_code=1)),
    ('missing-success', lambda rs, m: rs.pop()),
    ('missing-driver-verification', lambda rs, m: m.pop('driver_identities_verified')),
    ('false-driver-verification', lambda rs, m: m.update(driver_identities_verified=False)),
    ('changed-driver', lambda rs, m: m['driver_identities_after'].update({m['argv'][0]: '0'*64})),
    ('missing-truth-flag', lambda rs, m: first(rs, 'query_validation').pop('reference_bad')),
    ('boolean-truth-flag', lambda rs, m: first(rs, 'query_validation').update(reference_bad=False)),
    ('consistent-forged-topk', forge_consistent_topk),
]:
    cases.append(check(name, mutate))
with args.summary.open('x') as output:
    json.dump({'validator_selfcheck_passed': True, 'source_smoke': str(args.input),
               'not_database_performance_evidence': True, 'cases': cases}, output, indent=2)
    output.write('\n')
print('PASS: valid smoke accepted; all damaged copies rejected')
