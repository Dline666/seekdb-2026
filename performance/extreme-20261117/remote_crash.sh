#!/usr/bin/env bash
set -euo pipefail
export MVDB_LAB_ROOT=/home/platemo/seekdb-experiments/20261117/experiment-runtime
export MVDB_LAB_GO=/home/platemo/seekdb-experiments/20261117/toolchains/go/bin/go
run_prefix=${1:-remote-final}
for seed in 1 2 3; do
  /home/platemo/.local/bin/python3 performance/extreme-20261117/remote.py run "$run_prefix-crash-$seed" -- \
    env MVDB_TMP="$MVDB_LAB_ROOT" python3 minivectordb/tools/crash_test.py \
    --bin "$MVDB_LAB_ROOT/bin/remote-final" --rounds 40 --seed "$seed"
done
