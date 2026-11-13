"""Sensitivity scenarios, NOT the platform's unknown score conversion."""
import json
from pathlib import Path
import sys

root = Path(sys.argv[1])
summary = json.loads((root / 'summary.json').read_text(encoding='utf-8-sig'))
platform = json.loads((root / 'platform-scores.json').read_text(encoding='utf-8-sig'))
scores = {r['commit'][:7]: r['score'] for r in platform['records']}
lo, hi = scores['d7ed759'], scores['b44b5b4']
scenarios = []
for rows in sorted({r['rows'] for r in summary}):
    entries = {r['name']: r for r in summary if r['rows'] == rows}
    for base in ('kernel2', 'v4'):
        def e(name):
            return entries[name]['local_E'][base]['median']
        slope = (hi - lo) / (e('v4') - e('kernel2'))
        def extrapolate(name):
            return lo + slope * (e(name) - e('kernel2'))
        row = dict(rows=rows, local_reference=base,
                   candidate_score_scenario=extrapolate('release-safe'),
                   previous_candidate_scenario=extrapolate('candidate-fix'))
        if 'baseline89' in entries:
            row['third_anchor_actual'] = scores['89f6d54']
            row['third_anchor_predicted'] = extrapolate('baseline89')
            row['third_anchor_error'] = row['third_anchor_predicted'] - row['third_anchor_actual']
        scenarios.append(row)
print(json.dumps(dict(
    method='S_d7 + (S_v4-S_d7)*(E_candidate-E_d7)/(E_v4-E_d7)',
    interpretation='Unvalidated linear extrapolation around two observed commits; not an official formula, confidence interval, or guarantee.',
    limitations='Unknown platform T/B/M, fixed baseline, CPU and workload. Changing the local reference changes the extrapolation; the third historical commit is an out-of-fit check.',
    scenarios=scenarios), indent=2))
