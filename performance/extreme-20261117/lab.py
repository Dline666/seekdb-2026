"""Reproducible local experiments; never submits to the competition platform."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time

REPO = Path(__file__).resolve().parents[2]
OUT = Path(__file__).resolve().parent / 'results'
ROOT = Path(os.environ.get('MVDB_LAB_ROOT', '/tmp/seekdb-extreme-20261117'))
GO = Path(os.environ.get('MVDB_LAB_GO', '/tmp/seekdb-round3-go/bin/go'))
ENV = dict(os.environ, GOROOT=str(GO.parents[1]), CGO_ENABLED='0',
           GOOS='linux', GOARCH='amd64', GOAMD64='v1', GOMAXPROCS='2',
           GOGC='20', MVDB_SYNC='fdatasync', GOTOOLCHAIN='local', GOPROXY='off',
           PATH=str(GO.parent)+os.pathsep+os.environ.get('PATH',''))
for key in list(ENV):
    if key.startswith('MVDB_') and key != 'MVDB_SYNC':
        del ENV[key]

def sha(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()

def save(name, value):
    (OUT / name).write_text(json.dumps(value, indent=2, ensure_ascii=False) + '\n')

def run(label, argv, cwd=None, env=None, timeout=1800):
    info = dict(argv=list(map(str, argv)), cwd=str(cwd) if cwd else None,
                official_submissions=0)
    start = time.monotonic()
    print(label + ': start', flush=True)
    with (OUT / (label + '.txt')).open('x') as log:
        log.write(json.dumps(info) + '\n')
        log.flush()
        result = subprocess.run(info['argv'], cwd=cwd, env=env or ENV,
                                stdout=log, stderr=subprocess.STDOUT, timeout=timeout)
    info.update(exit_code=result.returncode, wall_s=time.monotonic()-start)
    save(label + '-run.json', info)
    print(label + ': exit=' + str(result.returncode), flush=True)
    result.check_returncode()

def snapshot(name, source):
    dest = ROOT / name
    shutil.copytree(source, dest)
    save(name + '-source.json', {str(p.relative_to(dest)): sha(p)
         for p in sorted(dest.rglob('*')) if p.is_file()})
    return dest

def build(name, source):
    dest = snapshot(name, source)
    binary = ROOT / 'bin' / name
    run(name + '-build', [GO, 'build', '-trimpath', '-ldflags=-s -w', '-o', binary,
                         './cmd/minivectordb'], dest)
    save(name + '-binary.json', {'sha256': sha(binary), 'path': str(binary)})

def bench(label, names, rows=10000, dim=128, queries=3000, rounds=5, extra=()):
    argv = [str(ROOT/'bin/tbm'), '-bins', names, '-bin-dir', str(ROOT/'bin'),
            '-data-dir', str(ROOT), '-rows', str(rows), '-dim', str(dim),
            '-queries', str(queries), '-rounds', str(rounds), *extra]
    identities = {str(ROOT/'bin'/n.split(':')[0]): sha(ROOT/'bin'/n.split(':')[0])
                  for n in names.split(',')}
    identities[str(ROOT/'bin/tbm')] = sha(ROOT/'bin/tbm')
    start = time.monotonic()
    print(label + ': start', flush=True)
    with (OUT/(label+'.jsonl')).open('x') as out, (OUT/(label+'.stderr.txt')).open('x') as err:
        p = subprocess.run(argv, env=ENV, stdout=out, stderr=err, timeout=1800)
    unchanged = identities == {k: sha(k) for k in identities}
    save(label+'-run.json', dict(argv=argv, exit_code=p.returncode,
         wall_s=time.monotonic()-start, binary_hashes=identities,
         identities_unchanged=unchanged, official_submissions=0))
    p.check_returncode()
    assert unchanged
    if rounds == 5:
        from analyze_tbm import analyze
        report = analyze(OUT/(label+'.jsonl'))
        save(label+'-summary.json', report)
        print(label+': '+json.dumps(report['versions']), flush=True)

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['init', 'build', 'bench', 'test'])
    parser.add_argument('name', nargs='?', default='base')
    parser.add_argument('--bins', default='base,quad')
    parser.add_argument('--rows', type=int, default=10000)
    parser.add_argument('--dim', type=int, default=128)
    parser.add_argument('--queries', type=int, default=3000)
    parser.add_argument('--rounds', type=int, default=5)
    args, extra = parser.parse_known_args()
    OUT.mkdir(exist_ok=True)
    if args.action == 'init':
        (ROOT/'bin').mkdir(parents=True, exist_ok=True)
        build('base', REPO/'minivectordb')
        driver = Path(__file__).parent/'tbm_eval.go'
        run('driver-build', [GO, 'build', '-o', ROOT/'bin/tbm', driver])
        run('environment', ['bash', '-lc', 'uname -a; lscpu; df -h /; /tmp/seekdb-round3-go/bin/go version'])
    elif args.action == 'build':
        build(args.name, REPO/'minivectordb')
    elif args.action == 'bench':
        bench(args.name, args.bins, args.rows, args.dim, args.queries, args.rounds, extra)
    elif args.action == 'test':
        run(args.name+'-tests', [GO, 'test', './...'], ROOT/args.name)
