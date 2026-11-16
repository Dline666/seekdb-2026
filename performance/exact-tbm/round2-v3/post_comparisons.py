from common import *

# The short Docker default comparison was noisy and showed a query regression.
# A longer query phase checks that unresolved concern; keep both sets of results.
subprocess.run(['python3',str(OUTPUT/'benchmark.py'),'docker-default-long',
                '--queries','5000'],env=ENV,check=True)
subprocess.run(['python3',str(OUTPUT/'check_correctness.py')],env=ENV,check=True)
subprocess.run(['python3',str(OUTPUT/'archive_retention.py')],env=ENV,check=True)
space=os.statvfs(ROOT)
assert space.f_bfree*space.f_frsize > 6_000_000_000, 'insufficient space for fresh bulk-update workflow'
subprocess.run(['python3',str(OUTPUT/'stress_verify.py'),'candidate',
                '--stress-label','candidate-fresh-large'],env=ENV,check=True)
