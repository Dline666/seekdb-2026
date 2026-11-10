# 提交到 seekdb 仓库 2026-competition-round2 分支时的根目录 Dockerfile。
# 构建上下文是分支根目录，源码位于 minivectordb/ 子目录。
# 配套 .dockerignore 只放行 minivectordb 的源码，避免把整个 seekdb 树发给构建器。

FROM ubuntu:24.04 AS build

RUN (apt-get update || (sed -i 's#http://\(archive\|security\).ubuntu.com#http://mirrors.aliyun.com#g' \
        /etc/apt/sources.list.d/ubuntu.sources && apt-get update)) \
 && apt-get install -y --no-install-recommends build-essential cmake \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY minivectordb/CMakeLists.txt ./
COPY minivectordb/src ./src
RUN cmake -S . -B /build -DCMAKE_BUILD_TYPE=Release -DMINIVECTORDB_WERROR=ON \
 && cmake --build /build -j"$(nproc)"

FROM ubuntu:24.04

RUN mkdir -p /data
WORKDIR /app
COPY --from=build /build/minivectordb /app/minivectordb

VOLUME ["/data"]
ENTRYPOINT ["/app/minivectordb"]
CMD ["--data-dir", "/data"]
