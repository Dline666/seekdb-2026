"""Local, serialized ROUND5 builds and comparisons (run as root in WSL)."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time

REPO = Path(__file__).resolve().parents[3]
ROOT = Path(os.environ.get('SEEKDB_ROUND5_ROOT', '/tmp/seekdb-round5'))
RESULTS = ROOT / 'results'
GO = '/tmp/seekdb-round3-go/bin/go'
ENV = dict(os.environ, GOROOT='/tmp/seekdb-round3-go', CGO_ENABLED='0',
           GOOS='linux', GOARCH='amd64', GOAMD64='v1', GOMAXPROCS='2',
           GOGC='20', MVDB_SYNC='fdatasync')
for key in list(ENV):
    if key.startswith('MVDB_') and key != 'MVDB_SYNC':
        del ENV[key]


def hashes(folder):
    return {str(p.relative_to(folder)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(folder.rglob('*')) if p.is_file() and '__pycache__' not in p.parts}


def dump(name, obj):
    (RESULTS / name).write_text(json.dumps(obj, indent=2) + '\n')


def run(name, argv, cwd=None, env=None):
    print(f'{name}: start', flush=True)
    start = time.time()
    with (RESULTS / (name + '.txt')).open('x') as out:
        out.write(json.dumps({'argv': argv, 'cwd': str(cwd), 'start': start}) + '\n')
        out.flush()
        p = subprocess.run(argv, cwd=cwd, env=env or ENV, stdout=out, stderr=subprocess.STDOUT)
        out.write(json.dumps({'exit_code': p.returncode, 'wall_s': time.time()-start}) + '\n')
    print(f'{name}: exit={p.returncode} wall={time.time()-start:.2f}s', flush=True)
    p.check_returncode()


def prepare():
    ROOT.mkdir()
    RESULTS.mkdir()
    (ROOT / 'bin').mkdir()
    shutil.copytree(REPO / 'minivectordb', ROOT / 'base' / 'minivectordb')
    review = REPO / 'performance/exact-tbm'
    dump('preserved-review-before.json', {
        'REVIEW_NEXT.md': hashlib.sha256((review / 'REVIEW_NEXT.md').read_bytes()).hexdigest(),
        'evidence': hashes(review / 'results/review-round4')})
    dump('environment.json', {
        'baseline_commit': 'fb6cd8b81f07a537d99f6dd34fc34c29b1ff9835',
        'branch': 'perf/exact-round5', 'build_environment': {k: ENV[k] for k in
            ['GOROOT','CGO_ENABLED','GOOS','GOARCH','GOAMD64','GOMAXPROCS','GOGC','MVDB_SYNC']},
        'compiler': subprocess.check_output([GO, 'version'], env=ENV, text=True).strip(),
        'kernel': os.uname().release,
        'cpu_info': Path('/proc/cpuinfo').read_text().split('\n\n')[0],
        'cgroup': {'cpu.max': '200000 100000', 'memory.max': 2147483648,
                   'memory.swap.max': 0, 'pids.max': 128},
        'official_submissions': 0, 'pushed': False,
        'policy': 'one performance workload at a time; independent truth outside cgroup'})
    build('base')
    # prepare must run on the unchanged starting source; a later checkout
    # must not silently relabel the retained changes as the baseline.
    expected = 'f45727b61cda9da39dacaa147c3aa57ac9c0d5f4e5cee86d1d5fe2f6ed4e30e0'
    assert hashlib.sha256((ROOT / 'bin/base').read_bytes()).hexdigest() == expected
    run('build-driver', [GO, 'build', '-trimpath', '-o', str(ROOT / 'bin/tbm'),
                        str(REPO / 'performance/exact-tbm/tbm_bench.go')])


def build(name):
    module = ROOT / name / 'minivectordb'
    dump(name + '-source-sha256.json', hashes(module))
    run(name + '-build', [GO, 'build', '-trimpath', '-ldflags=-s -w',
                         '-o', str(ROOT / 'bin' / name), './cmd/minivectordb'], module)
    dump(name + '-binary-sha256.json', {name: hashlib.sha256((ROOT / 'bin' / name).read_bytes()).hexdigest()})


def snapshot(name, files):
    module = ROOT / name / 'minivectordb'
    shutil.copytree(ROOT / 'base/minivectordb', module)
    for rel in files:
        p = module / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(REPO / 'minivectordb' / rel, p)
    build(name)


def compare(name, workload, rows, dim, queries, bins=None, data='uniform'):
    bins = bins or 'base,' + name
    label = name + '-' + workload
    argv = [str(ROOT / 'bin/tbm'), '-bins', bins, '-bin-dir', str(ROOT / 'bin'),
            '-rows', str(rows), '-dim', str(dim), '-queries', str(queries),
            '-rounds', '5', '-data', data]
    print(f'{label}: start', flush=True)
    start = time.time()
    with (RESULTS / (label + '.jsonl')).open('x') as out, (RESULTS / (label + '.stderr.txt')).open('x') as err:
        p = subprocess.run(argv, env=ENV, stdout=out, stderr=err)
    dump(label + '-run.json', {'argv': argv, 'exit_code': p.returncode,
                              'start': start, 'wall_s': time.time()-start})
    p.check_returncode()
    print(f'{label}: completed wall={time.time()-start:.2f}s', flush=True)


if __name__ == '__main__':
    ap = argparse.ArgumentParser()
    ap.add_argument('action', choices=['prepare','snapshot','compare','test','fallback'])
    ap.add_argument('name', nargs='?')
    ap.add_argument('files', nargs='*')
    a = ap.parse_args()
    if a.action == 'prepare':
        prepare()
    elif a.action == 'snapshot':
        snapshot(a.name, a.files)
    elif a.action == 'test':
        if a.name == 'preload':
            binary = str(ROOT / 'bin/preload-tests')
            run('preload-test-build', [GO, 'test', '-c', '-o', binary, './internal/engine'],
                ROOT / a.name / 'minivectordb')
            for symbol in ['fusedScanInt8AVXVNNI','fusedScanCandidatesAVXVNNI']:
                run('preload-disassembly-' + symbol, ['objdump', '-d',
                    '--disassemble=minivectordb/internal/engine.' + symbol + '.abi0', binary])
        run(a.name + '-tests', [GO, 'test', './...', '-count=1', '-v'], ROOT / a.name / 'minivectordb')
    elif a.action == 'fallback':
        run(a.name + '-fallback-tests', [GO, 'test', './internal/engine', '-count=1', '-v'],
            ROOT / a.name / 'minivectordb', dict(ENV, MVDB_NO_AVXVNNI='1'))
    elif a.action == 'compare':
        for workload in a.files or ['10k','100k','tail127','dim1024']:
            rows, dim, queries = {'10k': (10000,128,3000), '100k': (100000,256,600),
                                 '10k-long': (10000,128,15000),
                                 'tail127': (10000,127,1000), 'dim1024': (10000,1024,1000),
                                 'fallback128': (10000,128,3000),
                                 'outlier': (10000,128,500), 'constant': (10000,128,500)}[workload]
            bins = 'base:MVDB_NO_AVXVNNI=1,'+a.name+':MVDB_NO_AVXVNNI=1' if workload == 'fallback128' else None
            compare(a.name, workload, rows, dim, queries, bins,
                    workload if workload in ['outlier','constant'] else 'uniform')
