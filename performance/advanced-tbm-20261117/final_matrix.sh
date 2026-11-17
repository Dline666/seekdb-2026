#!/usr/bin/env bash
set -euo pipefail
cd /home/platemo/seekdb-experiments/20261117/repository
lab=performance/advanced-tbm-20261117/lab.py
py=/home/platemo/.local/bin/python3
prefix=${1:-final}
bench() { sudo -n taskset --cpu-list 18 "$py" "$lab" bench "$prefix-$1" --bins remote-final,advanced-final "${@:2}"; }
bench default-1 -rows 10000 -dim 128 -queries 1000 -seed 41117
bench default-2 -rows 10000 -dim 128 -queries 1000 -seed 41119
bench long -rows 10000 -dim 128 -queries 20000 -seed 41121
bench 100k -rows 100000 -dim 256 -queries 1000 -seed 41123
bench dim127 -rows 10000 -dim 127 -queries 5000 -seed 41125
bench dim1024 -rows 10000 -dim 1024 -queries 1000 -seed 41127
bench selective -rows 10000 -dim 128 -queries 5000 -seed 41129 -categories 100 -filter-percent 100
bench dense -rows 10000 -dim 128 -queries 5000 -seed 41131 -categories 1 -filter-percent 100
bench constant -rows 10000 -dim 128 -queries 1000 -seed 41133 -data constant
bench outlier -rows 10000 -dim 128 -queries 1000 -seed 41135 -data outlier
bench multiline -rows 10000 -dim 128 -queries 1000 -seed 41137 -format multiline
sudo -n taskset --cpu-list 18 "$py" "$lab" bench "$prefix-partial" --bins remote-final:MVDB_INDEX_MEM_MB=4,advanced-final:MVDB_INDEX_MEM_MB=4 -rows 10000 -dim 1024 -queries 200 -seed 41139
sudo -n taskset --cpu-list 18 "$py" "$lab" bench "$prefix-round7" --bins remote-base,advanced-final -rows 10000 -dim 128 -queries 5000 -seed 41141
bench docker -rows 10000 -dim 128 -queries 1000 -seed 41143 -transport docker -image-prefix seekdb-advanced-20261117:
bench docker-long -rows 10000 -dim 128 -queries 5000 -seed 41145 -transport docker -image-prefix seekdb-advanced-20261117:
bench docker-100k -rows 100000 -dim 256 -queries 1000 -seed 41147 -transport docker -image-prefix seekdb-advanced-20261117:
# Added after final profiling exposed a possible sustained-query CPU cost.
bench 100k-long -rows 100000 -dim 256 -queries 5000 -seed 41151
