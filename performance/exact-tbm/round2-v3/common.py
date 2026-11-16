from pathlib import Path
import hashlib
import json
import os
import shutil
import subprocess
import time

ROOT = Path('/tmp/seekdb-fable-v3-20261116')
OUTPUT = Path('/mnt/c/Users/27372/seekdb-fable-v3-experiments')
REPO = Path('/mnt/c/Users/27372/seekdb-review-fable-v3')
PREVIOUS = Path('/tmp/seekdb-fable-v2-eval-20261116')
GO = '/tmp/seekdb-round3-go/bin/go'
ENV = dict(os.environ, GOROOT='/tmp/seekdb-round3-go', CGO_ENABLED='0',
           GOOS='linux', GOARCH='amd64', GOAMD64='v1', GOMAXPROCS='2',
           GOGC='20', MVDB_SYNC='fdatasync', GOTOOLCHAIN='local', GOPROXY='off')
for key in list(ENV):
    if key.startswith('MVDB_') and key != 'MVDB_SYNC':
        del ENV[key]

def sha(p):
    with Path(p).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()

def save(name, value):
    (OUTPUT/name).write_text(json.dumps(value, indent=2, ensure_ascii=False, allow_nan=False)+'\n')

def command(label, argv, cwd=None, extra=None, timeout=600, expected=0):
    assert not (OUTPUT/(label+'.txt')).exists(), label+' already exists'
    start = time.time()
    info = dict(argv=list(map(str,argv)), cwd=str(cwd) if cwd else None,
                extra_env=extra or {}, expected_exit=expected, official_submissions=0)
    print(label+': start', flush=True)
    with (OUTPUT/(label+'.txt')).open('x') as out:
        out.write(json.dumps(info)+'\n')
        out.flush()
        p = subprocess.run(info['argv'], cwd=cwd, env=dict(ENV, **(extra or {})),
                           stdout=out, stderr=subprocess.STDOUT, timeout=timeout)
    info.update(exit_code=p.returncode, wall_s=time.time()-start)
    save(label+'-run.json', info)
    print(label+': exit='+str(p.returncode), flush=True)
    assert p.returncode == expected, label
    return info

def snapshot(name):
    target=ROOT/name/'minivectordb'
    assert not target.exists()
    shutil.copytree(REPO/'minivectordb', target)
    save(name+'-source.json', {str(p.relative_to(target)):sha(p)
         for p in sorted(target.rglob('*')) if p.is_file()})
    return target
