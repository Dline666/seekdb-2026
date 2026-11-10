#!/usr/bin/env bash
# 回归测试：
#   1. tests/*.sql 逐个运行，与同名 .expected 对比；
#   2. tests/restart 场景：step1 写入后退出，step2 在同一数据目录重新启动并校验。
# 默认跑容器；设 MINIVECTORDB_BIN 可直接跑本地二进制（无需 Docker）。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${MINIVECTORDB_IMAGE:-minivectordb:dev}"
failed=0

run_sql() {  # $1=数据目录 $2=SQL 文件 $3=输出文件
  local data="$1" sql="$2" out="$3"
  if [[ -n "${MINIVECTORDB_BIN:-}" ]]; then
    "$MINIVECTORDB_BIN" --protocol table --data-dir "$data" < "$sql" > "$out" 2>"$out.err"
  else
    docker run --rm -i --cpus=2 --memory=2g -v "$data:/data" "$image" \
      --protocol table < "$sql" > "$out" 2>"$out.err"
  fi
}

for sql in "$root"/tests/*.sql; do
  name="$(basename "${sql%.sql}")"
  expected="${sql%.sql}.expected"
  if [[ ! -f "$expected" ]]; then
    echo "SKIP (no expected): $name"
    continue
  fi

  data="$root/tmp/test-data/$name"
  out="$root/tmp/$name.out"
  rm -rf "$data"
  mkdir -p "$data"

  if ! run_sql "$data" "$sql" "$out"; then
    echo "FAIL (exit): $name" >&2
    cat "$out.err" >&2
    failed=1
    continue
  fi

  if diff -u "$expected" "$out"; then
    echo "PASS: $name"
  else
    echo "FAIL: $name" >&2
    failed=1
  fi
done

# 重启场景：同一数据目录先写后读
restart_dir="$root/tests/restart"
if [[ -f "$restart_dir/step1.sql" && -f "$restart_dir/step2.sql" ]]; then
  data="$root/tmp/test-data/restart"
  out="$root/tmp/restart.out"
  rm -rf "$data"
  mkdir -p "$data"

  if ! run_sql "$data" "$restart_dir/step1.sql" "$root/tmp/restart-step1.out"; then
    echo "FAIL (exit): restart step1" >&2
    cat "$root/tmp/restart-step1.out.err" >&2
    failed=1
  elif ! run_sql "$data" "$restart_dir/step2.sql" "$out"; then
    echo "FAIL (exit): restart step2" >&2
    cat "$out.err" >&2
    failed=1
  elif diff -u "$restart_dir/step2.expected" "$out"; then
    echo "PASS: restart"
  else
    echo "FAIL: restart" >&2
    failed=1
  fi
fi

exit "$failed"
