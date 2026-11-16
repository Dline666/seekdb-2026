from common import *
assert (OUTPUT/'candidate-fresh-large-interrupted-archive.json').exists()
space=os.statvfs(ROOT)
assert space.f_bfree*space.f_frsize>6_000_000_000
subprocess.run(['python3',str(OUTPUT/'stress_verify.py'),'candidate',
    '--stress-label','candidate-fresh-large-retry'],env=ENV,check=True)
print('fresh retry complete',flush=True)
