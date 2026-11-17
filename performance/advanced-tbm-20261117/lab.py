"""Advanced experiment harness: isolated outputs, immutable prior snapshots."""
import argparse
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

HERE=Path(__file__).resolve().parent
OUT=HERE/'results'
REPO=HERE.parents[1]
ROOT=Path('/home/platemo/seekdb-experiments/20261117')
RUN=Path(os.environ.get('MVDB_LAB_ROOT',str(ROOT/'experiment-runtime')))
GO=Path(os.environ.get('MVDB_LAB_GO',str(ROOT/'toolchains/go/bin/go')))
ENV=dict(os.environ,GOROOT=str(GO.parents[1]),CGO_ENABLED='0',GOOS='linux',GOARCH='amd64',GOAMD64='v1',GOMAXPROCS='2',GOGC='20',GOMEMLIMIT='1288490188',GOTOOLCHAIN='local',GOPROXY='off',PATH=str(GO.parent)+os.pathsep+os.environ.get('PATH',''))
for k in list(ENV):
 if k.startswith('MVDB_'):del ENV[k]
ENV['MVDB_SYNC']='fdatasync'

def sha(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def save(name,data):
 with (OUT/name).open('x') as f:json.dump(data,f,ensure_ascii=False,indent=2);f.write('\n')
def execute(label,argv,cwd=None,env=None,timeout=7200):
 info={'argv':list(map(str,argv)),'cwd':str(cwd or REPO),'utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'uid':os.getuid(),'diagnostic_only':True}
 start=time.monotonic(); print(label+': start',flush=True)
 with (OUT/(label+'.txt')).open('x') as f:
  f.write(json.dumps(info)+'\n');f.flush()
  p=subprocess.run(info['argv'],cwd=cwd or REPO,env=env or ENV,stdout=f,stderr=subprocess.STDOUT,timeout=timeout)
 info.update(exit_code=p.returncode,wall_s=time.monotonic()-start)
 save(label+'-run.json',info);print(label+': exit='+str(p.returncode),flush=True);p.check_returncode()
def build(label,source,pgo=None):
 assert os.getuid()!=0
 dest=RUN/label;shutil.copytree(source,dest)
 save(label+'-source.json',{str(p.relative_to(dest)):sha(p) for p in sorted(dest.rglob('*')) if p.is_file()})
 flags=['-pgo='+str(pgo)] if pgo else ['-pgo=off']
 execute(label+'-build',[GO,'build','-trimpath',*flags,'-ldflags=-s -w','-o',RUN/'bin'/label,'./cmd/minivectordb'],cwd=dest)
 save(label+'-binary.json',{'sha256':sha(RUN/'bin'/label),'source':str(dest),'go':str(GO),'pgo':str(pgo) if pgo else None})
def bench(label,bins,extra,rounds=5):
 assert os.getuid()==0 and (ROOT/'MIGRATION_COMPLETE.json').is_file()
 argv=[str(RUN/'bin/advanced-tbm'),'-bins',bins,'-bin-dir',str(RUN/'bin'),'-data-dir',str(RUN),'-rounds',str(rounds),'-cpu-affinity','16-17']+extra
 hashes={str(RUN/'bin'/n.split(':')[0]):sha(RUN/'bin'/n.split(':')[0]) for n in bins.split(',')}
 hashes[str(RUN/'bin/advanced-tbm')]=sha(RUN/'bin/advanced-tbm')
 if '-perf-wrapper' in extra:
  wrapper=extra[extra.index('-perf-wrapper')+1];hashes[wrapper]=sha(wrapper)
 meta={'argv':argv,'binary_hashes':hashes,'driver_allowed_cpus':sorted(os.sched_getaffinity(0)),'utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'diagnostic_only':rounds!=5 or '-diagnostic-dir' in extra,'official_submissions':0}
 meta['harness_sources']={str(p):sha(p) for p in [HERE/'lab.py',HERE/'driver.go',HERE/'analyze.py']}
 meta['loadavg_start']=Path('/proc/loadavg').read_text().strip()
 start=time.monotonic();print(label+': start',flush=True)
 with (OUT/(label+'.jsonl')).open('x') as out,(OUT/(label+'.stderr.txt')).open('x') as err:
  p=subprocess.run(argv,cwd=REPO,env=ENV,stdout=out,stderr=err)
 meta.update(exit_code=p.returncode,wall_s=time.monotonic()-start,identities_unchanged=all(sha(k)==v for k,v in hashes.items()),loadavg_end=Path('/proc/loadavg').read_text().strip())
 save(label+'-run.json',meta);p.check_returncode();assert meta['identities_unchanged']
 if not meta['diagnostic_only']:
  spec=importlib.util.spec_from_file_location('analyzer',HERE/'analyze.py');mod=importlib.util.module_from_spec(spec);spec.loader.exec_module(mod)
  report=mod.analyze(OUT/(label+'.jsonl'));save(label+'-summary.json',report)
  print(label+': '+json.dumps([{k:v for k,v in row.items() if k!='samples'} for row in report['versions']]),flush=True)
 else:print(label+': diagnostic PASS',flush=True)

def main():
 p=argparse.ArgumentParser();p.add_argument('action',choices=['build','run','bench']);p.add_argument('label');p.add_argument('--source',type=Path,default=REPO/'minivectordb');p.add_argument('--bins');p.add_argument('--rounds',type=int,default=5);p.add_argument('--pgo',type=Path)
 a,extra=p.parse_known_args()
 with (ROOT/'remote-experiment.lock').open('a') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  with (OUT/'commands.jsonl').open('a') as f:f.write(json.dumps({'argv':sys.argv,'utc':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'uid':os.getuid()})+'\n')
  if a.action=='build':build(a.label,a.source,a.pgo)
  elif a.action=='bench':bench(a.label,a.bins,extra,a.rounds)
  else:execute(a.label,extra[1:] if extra[:1]==['--'] else extra)
if __name__=='__main__':main()
