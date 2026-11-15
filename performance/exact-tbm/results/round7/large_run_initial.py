"""Run one complete five-round comparison; no other CPU workload may overlap."""
import argparse
import importlib.util
import json
import re
import subprocess
import time

from run import ENV, REPO, ROOT, RESULTS, dump


parser = argparse.ArgumentParser()
parser.add_argument('label')
parser.add_argument('candidate')
parser.add_argument('--fixture', default='/tmp/seekdb-round6/heap-stress/db/data.log')
parser.add_argument('--rows', type=int, default=550000)
parser.add_argument('--dim', type=int, default=1024)
parser.add_argument('--queries', type=int, choices=[6, 12], default=6)
parser.add_argument('--allow-small', action='store_true')
args = parser.parse_args()
assert all(re.fullmatch('[A-Za-z0-9_-]+', x) for x in [args.label, args.candidate])
label = args.label
argv = [str(ROOT/'bin/large'), '-bins', str(ROOT/'bin/base')+','+str(ROOT/'bin'/args.candidate),
        '-fixture', args.fixture, '-work-dir', str(ROOT/(label+'-private')),
        '-rows', str(args.rows), '-dim', str(args.dim), '-queries', str(args.queries)]
if args.allow_small:
    argv.append('-allow-small')
start = time.time()
print(label+': start', flush=True)
with (RESULTS/(label+'.jsonl')).open('x') as out, \
        (RESULTS/(label+'.stderr.txt')).open('x') as err, \
        (RESULTS/(label+'-run.json')).open('x') as meta:
    process = subprocess.run(argv, env=ENV, stdout=out, stderr=err)
    json.dump({'argv': argv, 'start': start, 'wall_s': time.time()-start,
               'exit_code': process.returncode}, meta, indent=2)
    meta.write('\n')
process.check_returncode()
spec = importlib.util.spec_from_file_location('large_analysis', REPO/'performance/exact-tbm/round7/large/analyze.py')
analysis = importlib.util.module_from_spec(spec)
spec.loader.exec_module(analysis)
report = analysis.summarize(RESULTS/(label+'.jsonl'), RESULTS/(label+'-run.json'), args.allow_small)
dump(label+'-summary.json', report)
for version in report['versions']:
    print(version['name'], {s: version['scopes'][s]['median'] for s in ['cold','replay','service_total']}, flush=True)
print(label+': PASS', flush=True)
