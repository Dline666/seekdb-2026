from common import *

driver=PREVIOUS/'bin/retention-repro-parameterized'
label='retention-v2-memory'
argv=[str(driver),'-bins',f'v2={ROOT}/bin/v2,memory={ROOT}/bin/memory',
      '-work-dir',str(ROOT/label),'-start-rows','160']
start=time.time()
print(label+': start',flush=True)
with (OUTPUT/(label+'.jsonl')).open('x') as out, (OUTPUT/(label+'.stderr.txt')).open('x') as err:
    p=subprocess.run(argv,env=ENV,stdout=out,stderr=err,timeout=300)
save(label+'-run.json',{'argv':argv,'exit_code':p.returncode,'wall_s':time.time()-start,
    'driver_sha256':sha(driver),'binary_sha256':{n:sha(ROOT/'bin'/n) for n in ['v2','memory']},
    'scope':'256MiB retention diagnostic, not a 2GiB formal evaluation'})
records=[json.loads(line) for line in (OUTPUT/(label+'.jsonl')).read_text().splitlines()]
ends={r['candidate']:r for r in records if r['phase']=='version_complete'}
assert p.returncode==1 and ends['v2']['telemetry']['memory.events']['oom']>0
assert ends['memory']['full_workflow_passed'] and ends['memory']['telemetry']['memory.events']['oom']==0
save(label+'-summary.json',{'versions':ends,'regression_reproduced_and_fixed':True})
print(label+': original OOM, memory fix passed',flush=True)

for label, extra in [
    ('docker-memory',['--bins','v2,memory']),
    ('docker-memory-multiline',['--bins','v2,memory','--format','multiline']),
    ('docker-poll',['--bins','candidate,candidate:MVDB_SPIN_US=1000']),
    ('docker-poll-multiline',['--bins','candidate,candidate:MVDB_SPIN_US=1000','--format','multiline']),
    ('docker-default',[]),
    ('docker-multiline',['--format','multiline']),
    ('docker-100k',['--rows','100000','--dim','256','--queries','200']),
    ('docker-v2-final',['--bins','v2,candidate']),
    ('native-default',['--transport','native']),
]:
    subprocess.run(['python3',str(OUTPUT/'benchmark.py'),label,*extra],env=ENV,check=True)
