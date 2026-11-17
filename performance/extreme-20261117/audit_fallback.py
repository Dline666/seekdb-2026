"""Audit scalar/SIMD correctness separately from bit-identical score comparisons."""
import argparse
import json
from pathlib import Path


def audit(path):
    lines = path.read_text().splitlines()
    command = json.loads(lines[0])
    rows = [json.loads(line) for line in lines[1:]]
    run = json.loads(path.with_name(path.stem + '-run.json').read_text())
    assert run['exit_code'] == 0 and run['argv'] == command['argv']
    queries = [r for r in rows if r['phase'] == 'query']
    names = list(dict.fromkeys(r['name'] for r in queries))
    assert len(names) == 2
    expected = [(r, n, p) for r in range(5)
                for n in (names if r % 2 == 0 else names[::-1])
                for p in ('write', 'query')]
    assert [(r['round'], r['name'], r['phase']) for r in rows] == expected
    assert all(r['reference_bad'] == 0 for r in rows)
    assert all(r['reference_checked'] == r['queries'] for r in queries)
    assert len({r['ordered_ids_hash'] for r in queries}) == 1
    assert all(r['max_oracle_relative_error'] <= 1e-12 for r in queries)
    assert all(r['memory_events']['oom'] == r['memory_events']['oom_kill'] == 0 for r in rows)
    assert all(r['memory_limit_bytes'] == 2 << 30 and r['memory_swap_max'] == 0 and
               r['cpu_max'] == '200000 100000' and r['sync'] == 'fdatasync' and
               r['cpu_affinity_allowed'] == '16-17' for r in rows)
    versions = []
    for name in names:
        rs = [r for r in queries if r['name'] == name]
        assert len({r['response_hash'] for r in rs}) == 1
        versions.append({
            'name': name, 'response_hash': rs[0]['response_hash'],
            'nonidentical_distance_count_per_round': [r['nonidentical_distance_count'] for r in rs],
            'sample_differences': rs[0]['distance_difference_samples'],
        })
    return {
        'validation_passed': True, 'raw': path.name, 'score_computed': False,
        'complete_interleaving': expected, 'ordered_ids_hash': queries[0]['ordered_ids_hash'],
        'independent_queries_checked': sum(r['reference_checked'] for r in queries),
        **{key: max(r[key] for r in queries) for key in
           ('max_oracle_abs_error', 'max_oracle_relative_error', 'max_oracle_ulp_error')},
        'versions': versions,
    }


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('raw', type=Path)
    args = parser.parse_args()
    print(json.dumps(audit(args.raw), indent=2))
