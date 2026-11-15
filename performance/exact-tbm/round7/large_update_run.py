"""Run the large indexed-update comparison serially, preserving every trial."""
import argparse
import hashlib
import importlib.util
import json
import re
import subprocess
import time

from run import ENV, REPO, ROOT, RESULTS, dump


parser = argparse.ArgumentParser()
parser.add_argument('label')
parser.add_argument('candidate')
parser.add_argument('--baseline', default='partial-balanced')
parser.add_argument('--fixture', default='/tmp/seekdb-round6/heap-stress/db/data.log')
parser.add_argument('--rows', type=int, default=550000)
parser.add_argument('--dim', type=int, default=1024)
parser.add_argument('--allow-small', action='store_true')
args = parser.parse_args()
if (RESULTS/(args.label+'-summary.json')).exists():
    raise FileExistsError('Summary label already exists: '+args.label)
assert all(re.fullmatch('[A-Za-z0-9_-]+', value)
           for value in [args.label, args.candidate, args.baseline])
assert args.baseline != args.candidate
driver = ROOT/'bin/large-update'
source = REPO/'performance/exact-tbm/round7/large_update/main.go'
argv = [str(driver), '-bins', str(ROOT/'bin'/args.baseline)+','+str(ROOT/'bin'/args.candidate),
        '-fixture', args.fixture, '-work-dir', str(ROOT/(args.label+'-private')),
        '-rows', str(args.rows), '-dim', str(args.dim)]
if args.allow_small:
    argv.append('-allow-small')
before = {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in [driver, source]}
start = time.time()
print(args.label+': start', flush=True)
with (RESULTS/(args.label+'.jsonl')).open('x') as out, \
        (RESULTS/(args.label+'.stderr.txt')).open('x') as err, \
        (RESULTS/(args.label+'-run.json')).open('x') as meta:
    process = subprocess.run(argv, env=ENV, stdout=out, stderr=err)
    after = {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in [driver, source]}
    verified = before == after
    json.dump({'argv': argv, 'start': start, 'wall_s': time.time()-start,
               'exit_code': process.returncode, 'driver_identities_before': before,
               'driver_identities_after': after, 'driver_identities_verified': verified}, meta, indent=2)
    meta.write('\n')
process.check_returncode()
if not verified:
    raise RuntimeError('Driver identity changed during comparison')
spec = importlib.util.spec_from_file_location('large_update_analysis', REPO/'performance/exact-tbm/round7/large_update/analyze.py')
analysis = importlib.util.module_from_spec(spec)
spec.loader.exec_module(analysis)
report = analysis.summarize(RESULTS/(args.label+'.jsonl'), RESULTS/(args.label+'-run.json'), args.allow_small)
dump(args.label+'-summary.json', report)
for version in report['versions']:
    print(version['name'], {scope: values['median'] for scope, values in version['scopes'].items()}, flush=True)
print(args.label+': PASS', flush=True)
