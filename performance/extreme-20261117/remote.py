"""Server continuation; new labels preserve all historical evidence."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import sys
import time

import lab

ROOT = Path('/home/platemo/seekdb-experiments/20261117')


def main():
    p = argparse.ArgumentParser()
    p.add_argument('action', choices=['build', 'bench', 'run'])
    p.add_argument('label')
    p.add_argument('--source', type=Path, default=lab.REPO / 'minivectordb')
    p.add_argument('--bins')
    p.add_argument('--rows', type=int, default=10000)
    p.add_argument('--dim', type=int, default=128)
    p.add_argument('--queries', type=int, default=1000)
    p.add_argument('--rounds', type=int, default=5)
    a, extra = p.parse_known_args()
    if lab.ROOT != ROOT / 'experiment-runtime' or lab.GO != ROOT / 'toolchains/go/bin/go':
        raise SystemExit('Set MVDB_LAB_ROOT and MVDB_LAB_GO to the server paths.')
    with (ROOT / 'remote-experiment.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with (lab.OUT / 'remote-commands.jsonl').open('a') as log:
            log.write(json.dumps(dict(utc=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
                                      argv=sys.argv, uid=os.getuid(), cwd=os.getcwd(),
                                      lab_root=str(lab.ROOT), go=str(lab.GO))) + '\n')
        if a.action == 'build':
            if os.getuid() == 0:
                raise SystemExit('Source snapshots and builds must run as platemo.')
            lab.build(a.label, a.source)
        elif a.action == 'bench':
            if not (ROOT / 'MIGRATION_COMPLETE.json').is_file():
                raise SystemExit('Migration is not complete; no scored measurement permitted.')
            if os.getuid() != 0:
                raise SystemExit('Use sudo -n for cgroup measurements.')
            lab.save(a.label + '-scheduling.json', {
                'driver_allowed_cpus': sorted(os.sched_getaffinity(0)),
                'migration_receipt': str(ROOT / 'MIGRATION_COMPLETE.json'),
                'cpu_quota': '200000 100000',
            })
            lab.bench(a.label, a.bins, a.rows, a.dim, a.queries, a.rounds, extra)
        else:
            if extra[:1] == ['--']:
                extra = extra[1:]
            lab.run(a.label, extra, timeout=3600)


if __name__ == '__main__':
    main()
