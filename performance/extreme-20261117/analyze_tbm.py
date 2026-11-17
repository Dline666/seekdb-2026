"""Require five complete interleaved pairs before reporting local T/B/M/E."""
import argparse
import json
from pathlib import Path
import statistics as stats

METRICS = [('t_s', .6, .001), ('b_bytes', .2, 1048576), ('m_bytes', .2, 1048576)]


def analyze(path):
    records = [json.loads(line) for line in path.read_text().splitlines()]
    run = json.loads(path.with_name(path.stem + '-run.json').read_text())
    assert run['exit_code'] == 0, 'driver did not finish'
    names = run['argv'][run['argv'].index('-bins')+1].split(',')
    assert len(names) == 2 and len(set(names)) == 2
    expected = [(r, n, phase) for r in range(5)
                for n in (names if r % 2 == 0 else names[::-1])
                for phase in ['write', 'query']]
    assert [(r['round'], r['name'], r['phase']) for r in records] == expected
    workload = {k: records[0][k] for k in ['rows','dim','queries','seed','data']}
    assert all(all(r[k] == v for k, v in workload.items()) for r in records)
    assert all(r['reference_bad'] == 0 for r in records)
    queries = [r for r in records if r['phase'] == 'query']
    assert all(r['reference_checked'] == workload['queries'] for r in queries)
    assert len({r['response_hash'] for r in queries}) == 1
    index = {(r['name'], r['phase'], r['round']): r for r in records}
    baseline = {p: {m: stats.median(index[names[0],p,r][m] for r in range(3))
                    for m, _, _ in METRICS} for p in ['write','query']}
    versions = []
    for name in names:
        phases = {p: {m: stats.median(index[name,p,r][m] for r in range(5))
                      for m in ['t_s','b_bytes','m_bytes','warmup_s']} for p in ['write','query']}
        scores = []
        for r in range(5):
            e = 0
            for p, pw in [('write',1/3), ('query',2/3)]:
                for m, w, eps in METRICS:
                    ratio = (baseline[p][m]+eps)/(index[name,p,r][m]+eps)
                    e += pw*w*ratio/(1+ratio)
            scores.append(e)
        versions.append(dict(name=name, **phases, local_E={'median': stats.median(scores), 'rounds': scores},
                             checked_queries=5*workload['queries']))
    paired = {p: {m: [index[names[1],p,r][m]/index[names[0],p,r][m] for r in range(5)]
                  for m, _, _ in METRICS} for p in ['write','query']}
    return {'file': path.name, 'workload': workload, 'rounds': 5,
            'complete_interleaving': True, 'reference_bad': 0,
            'response_hash': queries[0]['response_hash'], 'baseline_first_three_medians': baseline,
            'versions': versions, 'paired_candidate_over_base': paired}


if __name__ == '__main__':
    ap = argparse.ArgumentParser()
    ap.add_argument('files', nargs='+', type=Path)
    ap.add_argument('--output', type=Path, required=True)
    a = ap.parse_args()
    reports = [analyze(p) for p in a.files]
    a.output.write_text(json.dumps(reports, indent=2) + '\n')
    for report in reports:
        b, c = report['versions']
        print(report['file'], 'truth=PASS',
              f"write {b['write']['t_s']:.6f}->{c['write']['t_s']:.6f}",
              f"query {b['query']['t_s']:.6f}->{c['query']['t_s']:.6f}",
              f"E {b['local_E']['median']:.6f}->{c['local_E']['median']:.6f}")
