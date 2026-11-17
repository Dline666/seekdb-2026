#!/usr/bin/env bash
# Run serially as platemo. Only cgroup drivers are elevated.
set -euo pipefail
export MVDB_LAB_ROOT=/home/platemo/seekdb-experiments/20261117/experiment-runtime
export MVDB_LAB_GO=/home/platemo/seekdb-experiments/20261117/toolchains/go/bin/go
lab_dir=/home/platemo/seekdb-experiments/20261117/repository/performance/extreme-20261117
run_prefix=${1:-remote-final}
bench() {
  sudo -n env MVDB_LAB_ROOT="$MVDB_LAB_ROOT" MVDB_LAB_GO="$MVDB_LAB_GO" \
    taskset --cpu-list 18 /home/platemo/.local/bin/python3 "$lab_dir/remote.py" bench \
    "$run_prefix-$1" --bins remote-base,remote-final -cpu-affinity 16-17 "${@:2}"
}
bench default --queries 1000 -seed 20261118
bench seed9001 --queries 1000 -seed 9001
bench long --queries 5000 -seed 2028
bench 100k --rows 100000 --dim 256 --queries 500 -seed 991
bench tail127 --dim 127 --queries 1000 -seed 1271
bench dim1024 --dim 1024 --queries 500 -seed 10241
bench constant --queries 500 -data constant -seed 951
bench outlier --queries 500 -data outlier -seed 753
bench multiline --queries 1000 -format multiline -seed 357
sudo -n env MVDB_LAB_ROOT="$MVDB_LAB_ROOT" MVDB_LAB_GO="$MVDB_LAB_GO" \
  taskset --cpu-list 18 /home/platemo/.local/bin/python3 "$lab_dir/remote.py" run \
  "$run_prefix-fallback-audit" -- "$MVDB_LAB_ROOT/bin/remote-tbm-audit" \
  -bins remote-final:MVDB_NO_SIMD=1,remote-final -bin-dir "$MVDB_LAB_ROOT/bin" \
  -data-dir "$MVDB_LAB_ROOT" -cpu-affinity 16-17 -rows 10000 -dim 128 \
  -queries 1000 -rounds 5 -seed 459
python3 "$lab_dir/audit_fallback.py" "$lab_dir/results/$run_prefix-fallback-audit.txt" \
  > "$lab_dir/results/$run_prefix-fallback-audit-verified.json"
sudo -n env MVDB_LAB_ROOT="$MVDB_LAB_ROOT" MVDB_LAB_GO="$MVDB_LAB_GO" \
  taskset --cpu-list 18 /home/platemo/.local/bin/python3 "$lab_dir/remote.py" bench \
  "$run_prefix-partial4m" --bins remote-base:MVDB_INDEX_MEM_MB=4,remote-final:MVDB_INDEX_MEM_MB=4 \
  -cpu-affinity 16-17 --dim 1024 --queries 100 -seed 517
bench tail127-confirm --dim 127 --queries 5000 -seed 1279
