#!/usr/bin/env bash
# 按评测方式前台运行容器：保持 stdin 打开，stderr 单独可见。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${MINIVECTORDB_IMAGE:-minivectordb:dev}"
data="${MINIVECTORDB_DATA:-$root/tmp/data}"

mkdir -p "$data"

docker run --rm -i \
  --cpus=2 --memory=2g \
  -v "$data:/data" \
  "$image" "$@"
