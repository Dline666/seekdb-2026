"""Final local checks; run after the serialized comparisons have finished."""
import argparse
import importlib.util
import json
import subprocess
import time

from run import ENV, GO, REPO, ROOT, RESULTS, dump, run

ap = argparse.ArgumentParser()
ap.add_argument('name')
a = ap.parse_args()
name = a.name
binary = str(ROOT / 'bin' / name)
module = ROOT / name / 'minivectordb'
run(name + '-race', [GO, 'test', '-race', './internal/engine', './internal/types', '-count=1'],
    module, dict(ENV, CGO_ENABLED='1'))
run(name + '-crash', ['python3', str(REPO / 'minivectordb/tools/crash_test.py'),
                     '--bin', binary, '--rounds', '30'])
run(name + '-exact-edges', ['python3', str(REPO / 'performance/exact-tbm/exact_edges.py'),
                           '--bin-dir', str(ROOT / 'bin'), '--binary', name, '--assert-correct'])
run(name + '-protocol', ['python3', str(REPO / 'protocol_check/run.py'),
                         '--command', json.dumps([binary, '--data-dir', '{data_dir}', '--sync', 'fdatasync']),
                         '--output', str(ROOT / (name + '-protocol'))])
run('build-sqlcompare', [GO, 'build', '-trimpath', '-o', str(ROOT / 'bin/sqlcompare'),
                         str(REPO / 'performance/exact-tbm/sqlcompare/main.go')])
argv = [str(ROOT / 'bin/sqlcompare'), '-bins', binary,
        '-fixture', '/tmp/seekdb-round3-stress-final/db/data.log',
        '-work-dir', str(ROOT / (name + '-large-private')), '-rows', '550000', '-dim', '1024',
        '-rounds', '1', '-census-rounds', '1']
print(name + '-capacity: start', flush=True)
start = time.time()
with (RESULTS / (name+'-capacity.jsonl')).open('x') as out, (RESULTS / (name+'-capacity.stderr.txt')).open('x') as err:
    p = subprocess.run(argv, env=ENV, stdout=out, stderr=err)
dump(name+'-capacity-run.json', {'argv': argv, 'exit_code': p.returncode, 'wall_s': time.time()-start})
p.check_returncode()
records = [json.loads(line) for line in (RESULTS / (name+'-capacity.jsonl')).read_text().splitlines()]
assert records[-1]['phase'] == 'success' and records[-1]['reference_bad'] == 0
assert records[-1]['memory_events']['oom'] == 0 and records[-1]['memory_events']['oom_kill'] == 0
spec = importlib.util.spec_from_file_location('sql_validation', REPO / 'performance/exact-tbm/sqlcompare/analyze.py')
sql_validation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sql_validation)
groups, _ = sql_validation.read_runs([RESULTS / (name+'-capacity.jsonl')])
dump(name+'-capacity-summary.json', {
    'single_version_capacity_check': True, 'no_comparison_E': True,
    'validation': records[-1],
    'validated_service_totals': [{'suite': key[-1], 'runs': list(runs.values())} for key, runs in groups.items()],
    'phases': [r for r in records if r['phase'].endswith(('_startup','_query','_shutdown'))]})
print(name + '-capacity: PASS', flush=True)
