import contextlib
import json
import math
import os
from pathlib import Path
import random
import struct
import subprocess
import tempfile
import argparse

parser=argparse.ArgumentParser()
parser.add_argument('--binary', action='append')
parser.add_argument('--bin-dir', type=Path, default=Path('/tmp/seekdb-review-bin'))
parser.add_argument('--assert-correct', action='store_true')
args=parser.parse_args()

def report(row):
    print(json.dumps(row))
    if args.assert_correct and 'expected' in row:
        assert row['got']==row['expected'], row

@contextlib.contextmanager
def database(binary, no_prefilter=False):
    with tempfile.TemporaryDirectory(prefix='seekdb-edge-') as d:
        env=os.environ.copy()
        if no_prefilter: env['MVDB_NO_PREFILTER']='1'
        p=subprocess.Popen([str(args.bin_dir/binary),'--data-dir',d],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,env=env)
        assert json.loads(p.stdout.readline())=={'type':'ready'}
        seq=0
        def request(sql):
            nonlocal seq
            seq+=1
            p.stdin.write(json.dumps(dict(id=seq,op='execute',sql=sql,search_mode='exact'))+'\n')
            p.stdin.flush()
            r=json.loads(p.stdout.readline())
            assert r['ok'],r
            return r.get('rows')
        try: yield request
        finally:
            p.stdin.close()
            p.wait(timeout=10)
            p.stdout.close()

def f32(x): return struct.unpack('f',struct.pack('f',x))[0]
rng=random.Random(717)
dim=512
chosen=None
for _ in range(1000):
    v=f32(rng.uniform(1e8,1e10))
    qsum=qnorm=0.0
    for _ in range(dim):
        qsum+=v
        qnorm+=v*v
    cancellation=qnorm+(-2*v)*qsum+dim*v*v
    if cancellation>1e-4:
        chosen=v
        break
assert chosen is not None
print(json.dumps(dict(case='constant-expansion',value=chosen,dim=dim,exact_squared=0,expanded_squared=cancellation)))
for binary in args.binary or ['kernel2','candidate-fix']:
    for dim in [2,128]:
        z=json.dumps([0]*dim,separators=(',',':'))
        one=json.dumps([1]*dim,separators=(',',':'))
        with database(binary) as send:
            send(f'CREATE TABLE docs(id BIGINT PRIMARY KEY,category VARCHAR(8),embedding VECTOR({dim}))')
            send(f"INSERT INTO docs VALUES(-9223372036854775808,'a','{z}'),(1,'a','{one}')")
            ordinary=send('SELECT id FROM docs ORDER BY id')
            for where in [''," WHERE category='a'"]:
                got=send(f"SELECT id,L2_DISTANCE(embedding,'{z}') AS d FROM docs{where} ORDER BY d LIMIT 1")
                report(dict(case='minimum-bigint',binary=binary,dim=dim,where=where,stored=ordinary,got=got,expected=[[-9223372036854775808,0]]))
    for disabled in [False,True]:
        dim=512
        v=json.dumps([chosen]*dim,separators=(',',':'))
        with database(binary,disabled) as send:
            send(f'CREATE TABLE docs(id BIGINT PRIMARY KEY,category VARCHAR(8),embedding VECTOR({dim}))')
            values=','.join(f"({i},'a','{v}')" for i in range(300,0,-1))
            send('INSERT INTO docs VALUES '+values)
            for where in [''," WHERE category='a'"]:
                got=send(f"SELECT id,L2_DISTANCE(embedding,'{v}') AS d FROM docs{where} ORDER BY d LIMIT 10")
                report(dict(case='constant-ties',binary=binary,no_prefilter=disabled,value=chosen,where=where,got=got,expected=[[i,0] for i in range(1,11)]))
