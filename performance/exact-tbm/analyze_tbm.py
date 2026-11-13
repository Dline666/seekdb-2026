import collections
import json
import pathlib
import statistics
import sys

records = []
for path in sys.argv[1:]:
    with open(path, encoding='utf-8-sig') as stream:
        records.extend(json.loads(line) for line in stream if line.strip())
groups = collections.defaultdict(list)
for r in records:
    if r['reference_bad']:
        raise ValueError('incorrect exact result')
    groups[(r['rows'], r['dim'], r['queries'], r['name'], r['phase'])].append(r)

report = []
for rows, dim, queries in sorted({key[:3] for key in groups}):
    names = sorted({key[3] for key in groups if key[:3] == (rows, dim, queries)})
    baseline = {}
    for name in names:
        baseline[name] = {}
        for phase in ('write', 'query'):
            runs = groups[(rows,dim,queries,name,phase)]
            first = sorted(runs,key=lambda r:r['round'])[:3]
            baseline[name][phase] = {metric:statistics.median(r[metric] for r in first)
                                     for metric in ('t_s','b_bytes','m_bytes')}
    for name in names:
        entry = dict(rows=rows,dim=dim,queries=queries,name=name)
        tuples = collections.defaultdict(dict)
        for phase in ('write', 'query'):
            runs=groups[(rows,dim,queries,name,phase)]
            entry[phase]={metric:statistics.median(r[metric] for r in runs)
                          for metric in ('t_s','b_bytes','m_bytes','warmup_s')}
            entry[phase]['rounds']=len(runs)
            if phase=='query':
                entry['checked_queries']=sum(r['reference_checked'] for r in runs)
                entry['response_hashes']=sorted({r['response_hash'] for r in runs})
            for r in runs: tuples[r['round']][phase]=r
        entry['local_E']={}
        for base in ('kernel2','v4'):
            if base not in baseline: continue
            scores=[]
            for pair in tuples.values():
                value=0
                for phase,pweight in [('write',1/3),('query',2/3)]:
                    r=pair[phase]
                    for metric,weight,eps in [('t_s',.6,.001),('b_bytes',.2,1048576),('m_bytes',.2,1048576)]:
                        ratio=(baseline[base][phase][metric]+eps)/(r[metric]+eps)
                        value+=pweight*weight*ratio/(1+ratio)
                scores.append(value)
            entry['local_E'][base]={'median':statistics.median(scores),'rounds':scores}
        report.append(entry)
print(json.dumps(report,indent=2))
