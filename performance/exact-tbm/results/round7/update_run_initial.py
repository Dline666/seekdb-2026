"""Run five interleaved indexed-update trials, with no concurrent CPU workload."""
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
parser.add_argument('--baseline', default='base')
parser.add_argument('--rows', type=int, default=10000)
parser.add_argument('--dim', type=int, default=1024)
parser.add_argument('--index-mem-mb', type=int, default=4)
args = parser.parse_args()
assert all(re.fullmatch('[A-Za-z0-9_-]+', x)
           for x in [args.label, args.candidate, args.baseline])
assert args.baseline != args.candidate
label = args.label
driver = ROOT/'bin/update'
source = REPO/'performance/exact-tbm/round7/update/main.go'
argv = [str(driver), '-bins', str(ROOT/'bin'/args.baseline)+','+str(ROOT/'bin'/args.candidate),
        '-work-dir', str(ROOT/(label+'-private')), '-rows', str(args.rows),
        '-dim', str(args.dim), '-index-mem-mb', str(args.index_mem_mb), '-batch', '64']
identities = {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in [driver, source]}
start = time.time()
print(label+': start', flush=True)
with (RESULTS/(label+'.jsonl')).open('x') as out, \
        (RESULTS/(label+'.stderr.txt')).open('x') as err, \
        (RESULTS/(label+'-run.json')).open('x') as meta:
    process = subprocess.run(argv, env=ENV, stdout=out, stderr=err)
    json.dump({'argv': argv, 'start': start, 'wall_s': time.time()-start,
               'exit_code': process.returncode, 'driver_identities': identities}, meta, indent=2)
    meta.write('\n')
process.check_returncode()
assert identities == {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in [driver, source]}
spec = importlib.util.spec_from_file_location('update_analysis', REPO/'performance/exact-tbm/round7/update/analyze.py')
analysis = importlib.util.module_from_spec(spec)
spec.loader.exec_module(analysis)
report = analysis.summarize(RESULTS/(label+'.jsonl'), RESULTS/(label+'-run.json'))
dump(label+'-summary.json', report)
for version in report['versions']:
    print(version['name'], {scope: values['median'] for scope, values in version['scopes'].items()}, flush=True)
print(label+': PASS', flush=True)
