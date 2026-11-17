"""Verify integration identity and preservation; write only new evidence."""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import subprocess

HERE=Path(__file__).resolve().parent
ROOT=Path('/home/platemo/seekdb-experiments/20261117')
REPO=ROOT/'repository'
RUN=ROOT/'experiment-runtime'


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def files(p):
    return {str(f.relative_to(p)):sha(f) for f in sorted(p.rglob('*')) if f.is_file()}


if __name__=='__main__':
    snapshot=files(RUN/'advanced-final')
    workspace=files(REPO/'minivectordb')
    assert snapshot==workspace, 'worktree differs from tested immutable snapshot'
    expected='4fb46d9c21d40092c0b12e6be3f3cd8bcef012351a72511539e65481ce68f178'
    assert sha(RUN/'bin/advanced-final')==expected
    assert sha(RUN/'bin/docker-advanced-final')==expected
    assert sha(RUN/'bin/remote-final')=='d1bb32fdaf2e12ba0b5d02bd37d8c2ff072e611fa419994c4641ae652bf55541'
    gofmt=ROOT/'toolchains/go/bin/gofmt'
    sources=[str(p) for p in sorted((REPO/'minivectordb').rglob('*.go'))]
    checked=subprocess.run([str(gofmt),'-l',*sources],capture_output=True,text=True,check=True)
    assert not checked.stdout, checked.stdout
    before=json.loads((HERE/'results/protected-before.json').read_text())
    after={rel:sha(ROOT/rel) for rel in before}
    changes=[rel for rel in before if before[rel]!=after[rel]]
    result=dict(utc=datetime.now(timezone.utc).isoformat(),unchanged=not changes,
                checked_files=len(before),changed=changes,sha256=after)
    with (HERE/'results/protected-after.json').open('x') as f:
        json.dump(result,f,indent=2);f.write('\n')
    assert not changes, changes
    with (HERE/'results/final-workspace-identity.json').open('x') as f:
        json.dump(dict(snapshot='experiment-runtime/advanced-final',
            workspace='repository/minivectordb',identical=True,files=snapshot,
            native_and_docker_sha256=expected,gofmt_clean=True),f,indent=2)
        f.write('\n')
    status=subprocess.run(['git','status','--porcelain=v1'],cwd=REPO,
                          capture_output=True,text=True,check=True)
    with (HERE/'results/git-status-after.txt').open('x') as f:
        f.write(status.stdout)
    print('PASS: tested sources equal worktree, native equals Docker, gofmt clean,',
          len(before),'protected old files unchanged')
