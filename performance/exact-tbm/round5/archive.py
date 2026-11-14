"""Archive small evidence files and verify the retained source/review hashes."""
import hashlib
import json
from pathlib import Path
import shutil

from run import REPO, ROOT, RESULTS, dump, hashes


def normalized(folder):
    return {str(p.relative_to(folder)): hashlib.sha256(p.read_bytes().replace(b'\r\n', b'\n')).hexdigest()
            for p in sorted(folder.rglob('*')) if p.is_file()}


review = REPO / 'performance/exact-tbm'
preserved = {'REVIEW_NEXT.md': hashlib.sha256((review / 'REVIEW_NEXT.md').read_bytes()).hexdigest(),
             'evidence': hashes(review / 'results/review-round4')}
assert preserved == json.loads((RESULTS / 'preserved-review-before.json').read_text())
dump('preserved-review-after.json', dict(unchanged=True, **preserved))
actual = normalized(REPO / 'minivectordb')
expected = normalized(ROOT / 'final/minivectordb')
assert actual == expected, 'working source differs from tested final snapshot'
base = normalized(ROOT / 'base/minivectordb')
diffs = {name: sorted(p for p in set(base) | set(src) if base.get(p) != src.get(p))
         for name in ['preload','scratch','align','final']
         for src in [normalized(ROOT / name / 'minivectordb')]}
assert diffs['preload'] == ['internal/engine/fused_avxvnni_amd64.s']
assert diffs['align'] == ['internal/engine/fused_avxvnni_amd64.s']
assert 'internal/engine/fused_avxvnni_amd64.s' not in diffs['scratch']
assert set(diffs['final']) == set(diffs['align']) | set(diffs['scratch'])
dump('source-check.json', {'matches_tested_final': True, 'normalization': 'CRLF to LF only',
                           'files': actual, 'independent_candidate_differences': diffs})
shutil.copyfile(ROOT / 'final-protocol/result.json', RESULTS / 'final-protocol.json')
dump('driver-sha256.json', {
    'tbm_bench.go': hashlib.sha256((review / 'tbm_bench.go').read_bytes()).hexdigest(),
    'sqlcompare/main.go': hashlib.sha256((review / 'sqlcompare/main.go').read_bytes()).hexdigest(),
    'round5': hashes(review / 'round5'),
    'tbm_binary': hashlib.sha256((ROOT / 'bin/tbm').read_bytes()).hexdigest(),
    'sqlcompare_binary': hashlib.sha256((ROOT / 'bin/sqlcompare').read_bytes()).hexdigest()})
archive = review / 'results/round5'
archive.mkdir()
for p in sorted(RESULTS.iterdir()):
    if p.is_file():
        shutil.copyfile(p, archive / p.name)
print(f'archived {len(list(archive.iterdir()))} small evidence files; source and original review hashes verified')
