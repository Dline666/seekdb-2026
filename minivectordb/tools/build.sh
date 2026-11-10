#!/usr/bin/env bash
# 本地构建：默认 Debug + ASan/UBSan，--release 构建优化版本。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mode="debug"
case "${1:-}" in
  --release) mode="release" ;;
  --debug|"") mode="debug" ;;
  *) echo "usage: $0 [--debug|--release]" >&2; exit 2 ;;
esac

build_dir="$root/build/$mode"
if [[ "$mode" == "debug" ]]; then
  cmake -S "$root" -B "$build_dir" \
    -DCMAKE_BUILD_TYPE=Debug -DMINIVECTORDB_SANITIZE=ON -DMINIVECTORDB_WERROR=ON
else
  cmake -S "$root" -B "$build_dir" \
    -DCMAKE_BUILD_TYPE=Release -DMINIVECTORDB_WERROR=ON
fi
cmake --build "$build_dir" -j

echo "binary: $build_dir/minivectordb"
