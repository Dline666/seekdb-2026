"""Independent delivery checks: raw resources, formula, patch and preservation."""
from datetime import datetime, timezone
from decimal import Decimal, localcontext
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

HERE=Path(__file__).resolve().parent
ROOT=Path('/home/platemo/seekdb-experiments/20261117')
RUN=ROOT/'experiment-runtime'


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def tree(p):
    return {str(f.relative_to(p)):sha(f) for f in p.rglob('*') if f.is_file()}


def time_cost(row,mode):
    if mode=='legacy':
        return Decimal(str(row['t_s']))
    if mode=='whole_process' or row['phase']=='write':
        return Decimal(str(row['request_total_s']))+Decimal(str(row['writeback_s']))
    return sum(Decimal(str(row[k])) for k in ['startup_s','warmup_s','query_s','shutdown_s','writeback_s'])


if __name__=='__main__':
    results=json.loads((HERE/'RESULTS.json').read_text())
    assert results['status']=='completed'
    checked=0
    with localcontext() as ctx:
        ctx.prec=40
        for label,c in results['final_comparisons'].items():
            raw=[json.loads(x) for x in (HERE/'results'/(label+'.jsonl')).read_text().splitlines()]
            assert len(raw)==20
            for row in raw:
                assert row['cpu_max']=='200000 100000'
                assert row['memory_limit_bytes']==2147483648 and row['memory_swap_max']==0
                assert row['cpu_affinity_allowed']=='16-17' and row['gomaxprocs']==2
                assert row['device']=='259:0' and row['filesystem_device']=='259:4'
                assert row['sync']=='fdatasync' and row['search_mode']=='exact'
                assert row['memory_events']['oom']==row['memory_events']['oom_kill']==0
                assert row['b_bytes']==row['r_bytes']+row['w_bytes']
                assert row['reference_bad']==0 and not row['diagnostic_dir']
                checked+=1
            names=[v['name'] for v in c['versions']]
            ix={(r['name'],r['phase'],r['round']):r for r in raw}
            for mode in ['prepared','whole_process','legacy']:
                for round_no in range(5):
                    phases=[]
                    for phase in ['write','query']:
                        a,b=[ix[name,phase,round_no] for name in names]
                        e=Decimal(0)
                        for k,w,eps in [('t_s','.6','.001'),('b_bytes','.2','1048576'),('m_bytes','.2','1048576')]:
                            av=time_cost(a,mode) if k=='t_s' else Decimal(a[k])
                            bv=time_cost(b,mode) if k=='t_s' else Decimal(b[k])
                            ratio=(av+Decimal(eps))/(bv+Decimal(eps))
                            e+=Decimal(w)*ratio/(1+ratio)
                        phases.append(e)
                    score=(phases[0]+2*phases[1])/3
                    assert abs(float(score)-c['paired_E'][mode]['samples'][round_no])<1e-12
    source_manifest=json.loads((HERE/'sources/CANDIDATE_SOURCES.json').read_text())
    archive=HERE/'sources/CANDIDATE_SOURCES.tar.gz'
    assert sha(archive)==source_manifest['archive_sha256']
    actual={}
    with tarfile.open(archive,'r:gz') as t:
        for m in t:
            assert m.isfile()
            actual[m.name]=hashlib.sha256(t.extractfile(m).read()).hexdigest()
    assert actual==source_manifest['files']
    with tempfile.TemporaryDirectory(prefix='advanced-delivery-',dir=RUN) as tmp:
        tmp=Path(tmp)
        shutil.copytree(RUN/'remote-final',tmp/'minivectordb')
        p=subprocess.run(['patch','--batch','-p1','-i',str(HERE/'sources/FINAL_IMPLEMENTATION.patch')],
                         cwd=tmp,capture_output=True,text=True,check=True)
        assert tree(tmp/'minivectordb')==tree(RUN/'advanced-final')
        regenerated=tmp/'REPORT.md'
        subprocess.run([sys.executable,str(HERE/'write_report.py'),'--output',str(regenerated)],check=True)
        assert regenerated.read_bytes()==(HERE/'REPORT.md').read_bytes()
    protected=json.loads((HERE/'results/protected-before.json').read_text())
    assert all(sha(ROOT/name)==expected for name,expected in protected.items())
    assert tree(ROOT/'repository/minivectordb')==tree(RUN/'advanced-final')
    for name in ['RESEARCH.md','EXPERIMENTS.md','REPORT.md','RESULTS.json','REPRODUCE.md']:
        assert (HERE/name).stat().st_size>0
    record=dict(utc=datetime.now(timezone.utc).isoformat(),passed=True,
        final_groups=len(results['final_comparisons']),raw_phase_resource_checks=checked,
        independent_decimal_score_checks=len(results['final_comparisons'])*5*3,
        candidate_archive_files_checked=len(actual),protected_old_files_checked=len(protected),
        patch_recreates_final_snapshot=True,report_reproduces_exactly=True,
        worktree_equals_tested_snapshot=True)
    with (HERE/'results/delivery-audit.json').open('x') as f:
        json.dump(record,f,indent=2);f.write('\n')
    print(json.dumps(record))
