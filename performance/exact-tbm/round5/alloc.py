"""Allocation mechanism check, separate from the serialized T/B/M trials."""
import shutil
from run import GO, REPO, ROOT, run

for name in ['base','scratch']:
    module = ROOT / (name + '-alloc/minivectordb')
    shutil.copytree(ROOT / name / 'minivectordb', module)
    shutil.copyfile(REPO / 'performance/exact-tbm/round5/insert_alloc.go.txt',
                    module / 'internal/engine/round5_alloc_test.go')
    run(name + '-allocation-diagnostic', [GO, 'test', './internal/engine',
        '-run', '^TestRound5InsertAlloc$', '-count=1', '-v'], module)
