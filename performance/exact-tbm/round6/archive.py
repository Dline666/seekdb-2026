"""Verify the selected source and archive all small ROUND6 evidence, including losses."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil

from analyze import analyze
from run import REPO, ROOT, RESULTS, dump, hashes


def normalized(folder):
    return {p.relative_to(folder).as_posix(): hashlib.sha256(
        p.read_bytes().replace(b'\r\n', b'\n')).hexdigest()
        for p in sorted(folder.rglob('*')) if p.is_file() and '__pycache__' not in p.parts}


parser = argparse.ArgumentParser()
parser.add_argument('selected', choices=['heap', 'heapflat', 'wide', 'final'])
args = parser.parse_args()
name = args.selected
review = REPO / 'performance/exact-tbm'
preserved = {'REVIEW_NEXT.md': hashlib.sha256((review / 'REVIEW_NEXT.md').read_bytes()).hexdigest(),
             'evidence': hashes(review / 'results/review-round4')}
assert preserved == json.loads((RESULTS / 'preserved-review-before.json').read_text())
dump('preserved-review-after.json', dict(unchanged=True, **preserved))
actual = normalized(REPO / 'minivectordb')
expected = normalized(ROOT / name / 'minivectordb')
assert actual == expected, 'working source differs from selected tested snapshot'
base = normalized(ROOT / 'base/minivectordb')
names = ['heap', 'residual', 'flatrows', 'reader', 'bootstrap', 'heapflat', 'final', 'wide']
diffs = {n: sorted(p for p in set(base) | set(src) if base.get(p) != src.get(p))
         for n in names for src in [normalized(ROOT / n / 'minivectordb')]}
assert diffs['heap'] == ['internal/engine/topk.go', 'internal/engine/typed_topk_test.go']
assert not any('requestline' in p or p.endswith('select.go') for p in diffs['wide'])
dump('source-check.json', {'selected': name, 'matches_tested_snapshot': True,
                          'normalization': 'CRLF to LF only', 'files': actual,
                          'independent_candidate_differences': diffs})

reports = []
checked = {}
for meta in sorted(RESULTS.glob('*-run.json')):
    run = json.loads(meta.read_text())
    argv = run.get('argv', [])
    if not argv or Path(argv[0]) != ROOT / 'bin/tbm':
        continue
    assert '-bins' in argv
    raw = meta.with_name(meta.name.removesuffix('-run.json') + '.jsonl')
    report = analyze(raw)
    reports.append(report)
    for version in report['versions']:
        key = version['name']
        checked[key] = checked.get(key, 0) + version['checked_queries']
dump('all-comparisons.json', reports)
selected_checked = sum(n for key, n in checked.items() if key.split(':', 1)[0] == name)
dump('truth-counts.json', {'by_binary_and_environment': checked,
                         'selected': name, 'selected_protocol_topk_queries': selected_checked,
                         'all_comparisons': len(reports), 'all_reference_bad': 0,
                         'excludes': ['diagnostic queries', 'Go tests', 'capacity and stress checks']})
stress = json.loads((RESULTS / (name + '-stress-summary.json')).read_text())
assert stress['validation_passed'] is True
capacity = json.loads((RESULTS / (name + '-capacity-summary.json')).read_text())
assert capacity['validation']['phase'] == 'success'
for suffix in ['race', 'crash', 'exact-edges', 'protocol']:
    record = json.loads((RESULTS / (name + '-' + suffix + '.txt')).read_text().splitlines()[-1])
    assert record['exit_code'] == 0, suffix
shutil.copyfile(ROOT / (name + '-protocol/result.json'), RESULTS / (name + '-protocol.json'))
dump('driver-sha256.json', {
    'tbm_bench.go': hashlib.sha256((review / 'tbm_bench.go').read_bytes()).hexdigest(),
    'sqlcompare/main.go': hashlib.sha256((review / 'sqlcompare/main.go').read_bytes()).hexdigest(),
    'stress/main.go': hashlib.sha256((review / 'stress/main.go').read_bytes()).hexdigest(),
    'round6': hashes(review / 'round6'),
    'tbm_binary': hashlib.sha256((ROOT / 'bin/tbm').read_bytes()).hexdigest(),
    'sqlcompare_binary': hashlib.sha256((ROOT / 'bin/sqlcompare').read_bytes()).hexdigest()})
archive = review / 'results/round6'
archive.mkdir(exist_ok=True)
for p in sorted(RESULTS.iterdir()):
    if not p.is_file():
        continue
    target = archive / p.name
    if target.exists():
        assert target.read_bytes() == p.read_bytes(), f'archive already contains different evidence: {p.name}'
    else:
        shutil.copyfile(p, target)
print(f'archived {len(list(RESULTS.iterdir()))} evidence files; {len(reports)} complete comparisons; '
      f'{name}: {selected_checked} independently checked protocol TopK queries')
