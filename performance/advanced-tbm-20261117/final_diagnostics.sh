#!/usr/bin/env bash
set -euo pipefail
cd /home/platemo/seekdb-experiments/20261117/repository
adv="$PWD/performance/advanced-tbm-20261117"
run=/home/platemo/seekdb-experiments/20261117/experiment-runtime
go=/home/platemo/seekdb-experiments/20261117/toolchains/go/bin/go
py=/home/platemo/.local/bin/python3
"$py" "$adv/make_profile.py" "$run/advanced-final" advanced-final-profile-source
"$py" "$adv/lab.py" build advanced-final-profile --source "$run/advanced-final-profile-source"
sudo -n taskset --cpu-list 18 "$py" "$adv/lab.py" bench final-profile-default \
  --bins advanced-final-profile --rounds 1 -rows 10000 -dim 128 -queries 2000 -seed 11317 \
  -diagnostic-repeat 20 -diagnostic-dir "$adv/profiles/final-default"
sudo -n taskset --cpu-list 18 "$py" "$adv/lab.py" bench final-profile-100k \
  --bins advanced-final-profile --rounds 1 -rows 100000 -dim 256 -queries 1000 -seed 11319 \
  -diagnostic-repeat 10 -diagnostic-dir "$adv/profiles/final-100k"
"$py" "$adv/lab.py" run final-profile-query-top -- "$go" tool pprof -top \
  "$run/bin/advanced-final-profile" "$adv/profiles/final-default/round0-advanced-final-profile-query/query.cpu"
"$py" "$adv/lab.py" run final-profile-alloc-top -- "$go" tool pprof -top -alloc_space \
  -base "$adv/profiles/final-default/round0-advanced-final-profile-query/query-begin.heap" \
  "$run/bin/advanced-final-profile" "$adv/profiles/final-default/round0-advanced-final-profile-query/query-end.heap"
"$py" "$adv/lab.py" run final-profile-prepare-top -- "$go" tool pprof -top \
  "$run/bin/advanced-final-profile" "$adv/profiles/final-100k/round0-advanced-final-profile-query/prepare.cpu"
"$py" "$adv/lab.py" run final-escape-parser -- "$go" -C "$run/advanced-final" build \
  '-gcflags=minivectordb/internal/sqlparse=-m=2' -o "$run/bin/advanced-escape-parser" ./cmd/minivectordb
"$py" "$adv/lab.py" run final-escape-engine -- "$go" -C "$run/advanced-final" build \
  '-gcflags=minivectordb/internal/engine=-m=2 -d=ssa/check_bce/debug=1' -o "$run/bin/advanced-escape-engine" ./cmd/minivectordb
# Explicitly retain the expected baseline failure from the optional target
# probe; it is not a required Linux amd64 validation.
"$py" "$adv/lab.py" run baseline-arm64-probe -- env GOARCH=arm64 "$go" -C "$run/remote-final" build \
  -o "$run/bin/advanced-baseline-arm64-probe" ./cmd/minivectordb || true
