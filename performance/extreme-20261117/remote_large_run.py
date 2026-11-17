"""Run one complete five-round comparison; no other CPU workload may overlap."""
import argparse
import importlib.util
import json
import re
import subprocess
import time

from lab import ENV, REPO, ROOT, OUT as RESULTS, save as dump, sha
import fcntl
import os


parser = argparse.ArgumentParser()
parser.add_argument('label')
parser.add_argument('candidate')
parser.add_argument('--baseline', default='base')
parser.add_argument('--fixture', default='/tmp/seekdb-round6/heap-stress/db/data.log')
parser.add_argument('--rows', type=int, default=550000)
parser.add_argument('--dim', type=int, default=1024)
parser.add_argument('--queries', type=int, choices=[6, 12], default=6)
parser.add_argument('--allow-small', action='store_true')
args = parser.parse_args()
assert all(re.fullmatch('[A-Za-z0-9_-]+', x) for x in [args.label, args.candidate, args.baseline])
assert args.baseline != args.candidate
label = args.label
argv = [str(ROOT/'bin/remote-large'), '-bins', str(ROOT/'bin'/args.baseline)+','+str(ROOT/'bin'/args.candidate),
        '-fixture', args.fixture, '-work-dir', str(ROOT/(label+'-private')),
        '-rows', str(args.rows), '-dim', str(args.dim), '-queries', str(args.queries)]
if args.allow_small:
    argv.append('-allow-small')
assert (ROOT.parent/'MIGRATION_COMPLETE.json').is_file(), 'migration incomplete'
assert os.getuid() == 0, 'use sudo -n'
lock = (ROOT.parent/'remote-experiment.lock').open('a')
fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
start = time.time()
identity_paths = [ROOT/'bin/remote-large', ROOT/'bin'/args.baseline,
                  ROOT/'bin'/args.candidate,
                  REPO/'performance/extreme-20261117/remote_large/main.go',
                  REPO/'performance/extreme-20261117/remote_large/analyze.py']
identities = {str(path): sha(path) for path in identity_paths}
print(label+': start', flush=True)
with (RESULTS/(label+'.jsonl')).open('x') as out, \
        (RESULTS/(label+'.stderr.txt')).open('x') as err, \
        (RESULTS/(label+'-run.json')).open('x') as meta:
    process = subprocess.run(argv, env=ENV, stdout=out, stderr=err)
    unchanged = all(sha(path) == identities[str(path)] for path in identity_paths)
    json.dump({'argv': argv, 'start': start, 'wall_s': time.time()-start,
               'exit_code': process.returncode, 'identities': identities,
               'identities_unchanged': unchanged,
               'driver_allowed_cpus': sorted(os.sched_getaffinity(0)),
               'official_submissions': 0}, meta, indent=2)
    meta.write('\n')
process.check_returncode()
assert unchanged, 'binary, driver or analyzer changed during run'
spec = importlib.util.spec_from_file_location('large_analysis', REPO/'performance/extreme-20261117/remote_large/analyze.py')
analysis = importlib.util.module_from_spec(spec)
spec.loader.exec_module(analysis)
report = analysis.summarize(RESULTS/(label+'.jsonl'), RESULTS/(label+'-run.json'), args.allow_small)
dump(label+'-summary.json', report)
for version in report['versions']:
    print(version['name'], {s: version['scopes'][s]['median'] for s in ['cold','replay','service_total']}, flush=True)
print(label+': PASS', flush=True)
