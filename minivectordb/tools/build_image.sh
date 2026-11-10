#!/usr/bin/env bash
# 构建评测镜像（linux/amd64，多阶段从源码编译）。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${MINIVECTORDB_IMAGE:-minivectordb:dev}"

docker build --platform=linux/amd64 -t "$image" "$root"
echo "image: $image"
