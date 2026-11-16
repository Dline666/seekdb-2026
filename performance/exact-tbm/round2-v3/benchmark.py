import argparse
import statistics
from common import *
from analyze_tbm import analyze

parser=argparse.ArgumentParser()
parser.add_argument('label')
parser.add_argument('--bins', default='base,candidate')
parser.add_argument('--transport',default='docker', choices=['native','docker'])
parser.add_argument('--rows',type=int,default=10000)
parser.add_argument('--dim',type=int,default=128)
parser.add_argument('--queries',type=int,default=1000)
parser.add_argument('--format',default='compact')
parser.add_argument('--data',default='uniform')
parser.add_argument('--cpu-count',type=int,default=2)
parser.add_argument('--smoke',action='store_true')
a=parser.parse_args()
assert a.label.replace('-','').isalnum()
assert not (OUTPUT/(a.label+'.jsonl')).exists()
paths=[ROOT/'bin'/n.split(':')[0] for n in a.bins.split(',')]
identities={str(p):sha(p) for p in paths+[ROOT/'bin/tbm-eval',OUTPUT/'tbm_eval.go']}
argv=[str(ROOT/'bin/tbm-eval'),'-bins',a.bins,'-bin-dir',str(ROOT/'bin'),'-data-dir',str(ROOT),
      '-transport',a.transport,'-rows',str(a.rows),'-dim',str(a.dim),'-queries',str(a.queries),
      '-rounds','5','-format',a.format,'-data',a.data,'-cpu-count',str(a.cpu_count)]
print(a.label+': start',flush=True)
start=time.time()
with (OUTPUT/(a.label+'.jsonl')).open('x') as out, (OUTPUT/(a.label+'.stderr.txt')).open('x') as err:
    p=subprocess.run(argv,env=ENV,stdout=out,stderr=err,timeout=1200)
unchanged=identities=={p:sha(p) for p in identities}
save(a.label+'-run.json', {'argv':argv,'exit_code':p.returncode,'wall_s':time.time()-start,
    'identities':identities,'identities_unchanged':unchanged,'official_submissions':0})
assert unchanged
p.check_returncode()
records=[json.loads(line) for line in (OUTPUT/(a.label+'.jsonl')).read_text().splitlines()]
for r in records:
    assert r['transport']==a.transport and r['memory_events']['oom']==r['memory_events']['oom_kill']==0
    if a.transport=='docker':
        assert r['container_cgroup'].startswith('0::/seekdbtbm')
    cpu=dict(line.split() for line in r['cpu_stat'].splitlines())
    r['cpu_usage_s']=int(cpu['usage_usec'])/1e6
    r['cpu_throttled_s']=int(cpu['throttled_usec'])/1e6
extras={}
for name in a.bins.split(','):
    extras[name]={}
    for phase in ['write','query']:
        rows=[r for r in records if r['name']==name and r['phase']==phase]
        extras[name][phase]={k:statistics.median(r[k] for r in rows) for k in
            ['startup_s','ddl_s','load_s','warmup_s','query_s','shutdown_s',
             'request_total_s','cpu_usage_s','cpu_throttled_s']}
report=analyze(OUTPUT/(a.label+'.jsonl'))
report.update(transport=a.transport,format=a.format,cpu_count=a.cpu_count,
    extra_phase_medians=extras,smoke_only=a.smoke,memory_events_zero=True,
    binary_identities=identities,images=json.loads((OUTPUT/'images.json').read_text()))
save(a.label+'-summary.json',report)
b,c=report['versions']
print(a.label+': PASS '+json.dumps({'write':[b['write'],c['write']],
    'query':[b['query'],c['query']], 'local_E':[b['local_E']['median'],c['local_E']['median']]}),flush=True)
