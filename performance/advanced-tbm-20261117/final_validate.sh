#!/usr/bin/env bash
set -euo pipefail
cd /home/platemo/seekdb-experiments/20261117/repository
adv=/home/platemo/seekdb-experiments/20261117/repository/performance/advanced-tbm-20261117
run=/home/platemo/seekdb-experiments/20261117/experiment-runtime
go=/home/platemo/seekdb-experiments/20261117/toolchains/go/bin/go
py=/home/platemo/.local/bin/python3

"$py" "$adv/lab.py" run final-stress-driver-build -- "$go" build -trimpath -o "$run/bin/advanced-stress" "$adv/stress.go"
"$py" "$adv/lab.py" run final-capacity-smoke -- sudo -n taskset --cpu-list 18 "$run/bin/advanced-stress" \
  -binary "$run/bin/advanced-final" -data-dir "$run/final-capacity-smoke" \
  -rows 128 -dim 1024 -queries 2 -batch 64 -bulk-update -recovery-kill-delay 10ms
"$py" "$adv/lab.py" run final-edges -- "$py" performance/exact-tbm/exact_edges.py \
  --binary advanced-final --bin-dir "$run/bin" --assert-correct
"$py" "$adv/lab.py" run final-protocol -- "$py" protocol_check/run.py \
  --command '["/home/platemo/seekdb-experiments/20261117/experiment-runtime/bin/advanced-final","--data-dir","{data_dir}"]' \
  --output "$adv/results/final-public-native"
"$py" "$adv/lab.py" run final-protocol-docker -- "$py" protocol_check/run.py \
  --image seekdb-advanced-20261117:advanced-final --output "$adv/results/final-public-docker"
for seed in 1 2 3; do
  "$py" "$adv/lab.py" run "final-crash-$seed" -- env "MVDB_TMP=$run" "$py" minivectordb/tools/crash_test.py \
    --bin "$run/bin/advanced-final" --rounds 40 --seed "$seed"
done
"$py" "$adv/lab.py" run final-fallback-oracle -- sudo -n taskset --cpu-list 18 "$run/bin/remote-tbm-audit" \
  -bins advanced-final,advanced-final:MVDB_NO_SIMD=1 -bin-dir "$run/bin" -data-dir "$run" \
  -rounds 5 -rows 1000 -dim 127 -queries 200 -seed 41149 -cpu-affinity 16-17
"$py" - <<'PY'
import json, subprocess
from pathlib import Path
p=Path('performance/advanced-tbm-20261117/results/final-fallback-oracle.txt')
r=subprocess.run(['/home/platemo/.local/bin/python3','performance/extreme-20261117/audit_fallback.py',str(p)],capture_output=True,text=True,check=True)
j=json.loads(r.stdout)
with p.with_name('final-fallback-audit.json').open('x') as f:
 json.dump(j,f,indent=2);f.write('\n')
print('fallback independent audit PASS',j['independent_queries_checked'])
PY
sudo -n taskset --cpu-list 18 "$py" "$adv/stress_run.py" advanced-final --stress-label final-capacity
sudo -n taskset --cpu-list 18 "$py" "$adv/large_run.py" final-large advanced-final --baseline remote-final --queries 12
