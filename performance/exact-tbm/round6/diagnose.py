"""Count query work and allocations in a temporary, instrumented copy only."""
import argparse
from pathlib import Path
import shutil

from run import ROOT, REPO, ENV, GO, run

ap = argparse.ArgumentParser()
ap.add_argument('name')
a = ap.parse_args()
module = ROOT / (a.name + '-diagnostic') / 'minivectordb'
shutil.copytree(ROOT / a.name / 'minivectordb', module)
source = module / 'internal/engine/l2_amd64.go'
text = source.read_text()
signature = 'func l2sqGatherBytes(a []float64, base []byte, offs []int64, out []float64) {'
assert text.count(signature) == 1
source.write_text(text.replace(signature, signature + '\n reviewExactRows.Add(int64(len(offs)))'))
shutil.copyfile(REPO / 'performance/exact-tbm/round6/query-diagnostic.go.txt',
                module / 'internal/engine/review_diag_test.go')
run(a.name + '-query-work', [GO, 'test', './internal/engine', '-run',
    '^TestRound6QueryDiagnostic$', '-count=1', '-v'], module)
