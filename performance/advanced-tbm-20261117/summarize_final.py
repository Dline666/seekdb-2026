"""Recompute paired E from immutable raw samples, preserving earlier anchors.

Run only after all validation has finished. Output is exclusive: use a fresh
--output for a reproduction. No performance measurements happen here.
"""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
from statistics import median

HERE = Path(__file__).resolve().parent
METRICS = [('t_s', .6, .001), ('b_bytes', .2, 1048576),
           ('m_bytes', .2, 1048576)]
FINAL_LABELS = ['default-1', 'default-2', 'long', '100k', 'dim127',
                'dim1024', 'selective', 'dense', 'constant', 'outlier',
                'multiline', 'partial', 'round7', 'docker', 'docker-long',
                'docker-100k', '100k-long']


def spread(values):
    center = median(values)
    return dict(median=center, min=min(values), max=max(values),
                mad=median(abs(x-center) for x in values), samples=values)


def elapsed(row, mode):
    if mode == 'legacy':
        return row['t_s']
    if mode == 'whole_process' or row['phase'] == 'write':
        return row['request_total_s'] + row.get('writeback_s', 0)
    return sum(row[k] for k in ['startup_s', 'warmup_s', 'query_s',
                                'shutdown_s']) + row.get('writeback_s', 0)


def phase_e(base, cand, mode):
    result = 0.
    for key, weight, eps in METRICS:
        a = elapsed(base, mode) if key == 't_s' else base[key]
        b = elapsed(cand, mode) if key == 't_s' else cand[key]
        # f((a+eps)/(b+eps)), evaluated without an intermediate division.
        result += weight * (a+eps) / (a+b+2*eps)
    return result


def enrich(summary):
    a, b = summary['versions']
    result = dict(summary)
    result['paired_E'] = {}
    for mode in ['prepared', 'whole_process', 'legacy']:
        scores = []
        phase_scores = {'write': [], 'query': []}
        for r in range(5):
            for phase in ['write', 'query']:
                phase_scores[phase].append(phase_e(a['samples'][phase][r],
                                                  b['samples'][phase][r], mode))
            scores.append((phase_scores['write'][r] +
                           2*phase_scores['query'][r])/3)
        result['paired_E'][mode] = dict(spread(scores),
            baseline_each_round=.5,
            rounds_above_baseline=sum(x > .5 for x in scores),
            phases={p: spread(v) for p, v in phase_scores.items()})
    result['complete_metric_distributions'] = {}
    result['paired_changes_percent'] = {}
    for phase in ['write', 'query']:
        result['paired_changes_percent'][phase] = {}
        for mode in ['prepared', 'whole_process', 'legacy']:
            for key, _, _ in METRICS:
                label = mode+'_t_s' if key == 't_s' else key
                values = []
                for r in range(5):
                    ar, br = a['samples'][phase][r], b['samples'][phase][r]
                    av = elapsed(ar, mode) if key == 't_s' else ar[key]
                    bv = elapsed(br, mode) if key == 't_s' else br[key]
                    values.append(100*(bv/av-1))
                result['paired_changes_percent'][phase][label] = spread(values)
        for version in [a, b]:
            distributions = result['complete_metric_distributions'].setdefault(version['name'], {})
            distributions[phase] = {
                key: spread([row[key] for row in version['samples'][phase]])
                for key in ['t_s', 'b_bytes', 'm_bytes', 'warmup_s',
                            'startup_s', 'shutdown_s', 'request_total_s']}
            for mode in ['prepared', 'whole_process']:
                distributions[phase][mode+'_t_s'] = spread([
                    elapsed(row, mode) for row in version['samples'][phase]])
    return result


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--prefix', default='final')
    ap.add_argument('--output', type=Path, default=HERE/'RESULTS.json')
    args = ap.parse_args()
    results = HERE/'results'
    comparisons = {}
    for path in sorted(results.glob('*-summary.json')):
        value = json.loads(path.read_text())
        if len(value.get('versions', [])) == 2 and 'samples' in value['versions'][0]:
            comparisons[path.name.removesuffix('-summary.json')] = enrich(value)
    final_names = [args.prefix+'-'+x for x in FINAL_LABELS]
    assert all(n in comparisons for n in final_names), 'matrix incomplete'
    for name in final_names:
        item = comparisons[name]
        assert item['rounds'] == 5 and item['reference_bad'] == 0
        assert item['post_exit_sync_time_recorded']
    capacity = json.loads((results/(args.prefix+'-capacity-summary.json')).read_text())
    large = json.loads((results/(args.prefix+'-large-summary.json')).read_text())
    assert capacity['validation_passed'] and large['validation_passed']
    large_a,large_b=[v['scopes']['service_total']['runs'] for v in large['versions']]
    large_scores=[sum(w*(a[k]+eps)/(a[k]+b[k]+2*eps)
                      for k,w,eps in METRICS) for a,b in zip(large_a,large_b)]
    large['paired_service_E']=dict(spread(large_scores),baseline_each_round=.5,
        scope='query service only; not overall write/query E')
    large['paired_service_changes_percent']={k:spread([
        100*(b[k]/a[k]-1) for a,b in zip(large_a,large_b)]) for k,_,_ in METRICS}
    validations = {}
    for label in ['final-tests', 'final-race', 'final-fallback', 'selected-lint-v2',
                  'final-edges', 'final-protocol', 'final-protocol-docker',
                  'final-fallback-oracle', 'final-crash-1', 'final-crash-2',
                  'final-crash-3', 'final-workspace-check']:
        entry = json.loads((results/(label+'-run.json')).read_text())
        assert entry['exit_code'] == 0, label
        validations[label] = entry
    protected = json.loads((results/'protected-after.json').read_text())
    assert protected['unchanged']
    failures = {}
    for path in sorted(results.glob('*-run.json')):
        entry = json.loads(path.read_text())
        if entry.get('exit_code') not in (0, None) or entry.get('error'):
            failures[path.name] = entry
    result = dict(schema_version=1, status='completed',
        generated_utc=datetime.now(timezone.utc).isoformat(),
        candidate='advanced-final',
        agent_session=json.loads((results/'session-mode.json').read_text()),
        candidate_sha256='4fb46d9c21d40092c0b12e6be3f3cd8bcef012351a72511539e65481ce68f178',
        baseline='remote-final',
        baseline_sha256='d1bb32fdaf2e12ba0b5d02bd37d8c2ff072e611fa419994c4641ae652bf55541',
        go='1.22.2', pgo=False, gogc=20, gomemlimit=1288490188,
        limits=dict(cpu_max='200000 100000', memory_max=2147483648,
                    memory_swap_max=0, sync='fdatasync', db_cpus=[16,17],
                    driver_cpu=18, physical_io_device='259:0'),
        official_submissions=0, github_pushes=0,
        score_scope='local relative E only; no official fixed baseline known',
        score_definition=dict(T_weight=.6, B_weight=.2, M_weight=.2,
            phase_weights=[1/3,2/3], epsilon_T_s=.001, epsilon_B_M_bytes=1048576,
            primary='paired prepared E, baseline 0.5 within each round',
            alternative='whole_process includes duplicate query-fixture load',
            legacy='phase t_s only, retained for historical comparison',
            earlier_anchors='original first-three-median summaries preserved'),
        final_comparisons={name: comparisons[name] for name in final_names},
        exploratory_comparisons={k:v for k,v in comparisons.items() if k not in final_names},
        large_query_service=large, capacity=capacity, validations=validations,
        diagnostic_profiles=json.loads((results/'profile-summary.json').read_text()),
        source_identity=json.loads((results/'final-workspace-identity.json').read_text()),
        artifacts=dict(final_patch='sources/FINAL_IMPLEMENTATION.patch',
                       all_candidate_sources='sources/CANDIDATE_SOURCES.tar.gz',
                       candidate_hashes='sources/CANDIDATE_SOURCES.json',
                       commands='results/commands.jsonl',
                       citations='sources/references.json'),
        fallback_audit=json.loads((results/'final-fallback-audit.json').read_text()),
        failed_and_interrupted_runs=failures, protected_evidence=protected,
        limitations=[
            'five paired samples quantify observed noise, not a confidence guarantee',
            'no cross-workload weights invented; no combined official score',
            'large service E is query-only and cannot replace write/query total E',
            'kernel memory.peak is cumulative; large tests use fresh cgroups',
            'optional arm64 probe failed on pre-existing VNNI fallback symbols',
            'SIGKILL verifies process crashes, not physical power-loss hardware',
            'no changes to persistent storage selected; failed storage experiments retained'])
    with args.output.open('x') as f:
        json.dump(result, f, ensure_ascii=False, indent=2)
        f.write('\n')
    for name in final_names:
        x = comparisons[name]
        a,b = x['versions']
        print(name, 'prepared E',round(x['paired_E']['prepared']['median'],6),
              'whole E',round(x['paired_E']['whole_process']['median'],6),
              'query',a['prepared_T']['query'],b['prepared_T']['query'])


if __name__ == '__main__':
    main()
