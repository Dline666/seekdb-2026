from common import *
binary=ROOT/'bin/candidate'
command('candidate-protocol-docker', ['python3',REPO/'protocol_check/run.py',
    '--image','seekdb-local-v3:candidate',
    '--output',ROOT/'protocol-docker'], timeout=300)
command('candidate-crash',['python3',REPO/'minivectordb/tools/crash_test.py','--bin',binary,'--rounds','30'])
command('candidate-exact-edges',['python3',REPO/'performance/exact-tbm/exact_edges.py',
    '--bin-dir',ROOT/'bin','--binary','candidate','--assert-correct'])
for label,extra in [('ordinary',[]),('chunked',['--chunked','--cand-env','MVDB_FAST_INSERT_MIN=0','--cand-env','MVDB_FAST_INSERT_WORKERS=2'])]:
    command('differential-'+label,['python3',REPO/'performance/lead-opt/diff_fuzz.py',
        ROOT/'bin/v2',binary,'--seeds','4101-4125','--steps','400',*extra],cwd=ROOT)

# The unchanged literal parser still receives the independently implemented Rat oracle.
diagnostic=ROOT/'numeric-oracle/minivectordb'
shutil.copytree(ROOT/'candidate/minivectordb',diagnostic)
shutil.copyfile(OUTPUT.parent/'seekdb-fable-v2-evaluation/diagnostics/numeric_test.go',
               diagnostic/'internal/types/evaluation_numeric_test.go')
command('independent-numeric-oracle',[GO,'test','./internal/types','-run','^TestFableDiagnostic','-count=1','-v'],cwd=diagnostic)
