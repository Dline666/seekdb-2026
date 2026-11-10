# syntax=docker/dockerfile:1

# 构建阶段：从源码编译，依赖全部在构建期安装（正式评测不允许联网）。
FROM ubuntu:24.04 AS build

# 先按默认源安装；失败的发行版/网络环境下切到镜像源重试一次。
RUN set -eux; \
    apt-get update || true; \
    if ! apt-get install -y --no-install-recommends build-essential cmake; then \
      sed -i 's#http://\(archive\|security\).ubuntu.com#http://mirrors.aliyun.com#g' \
        /etc/apt/sources.list.d/ubuntu.sources; \
      apt-get update; \
      apt-get install -y --no-install-recommends build-essential cmake; \
    fi; \
    rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY CMakeLists.txt ./
COPY src ./src
RUN cmake -S . -B /build -DCMAKE_BUILD_TYPE=Release -DMINIVECTORDB_WERROR=ON \
 && cmake --build /build -j"$(nproc)"

# 运行阶段：只保留二进制与 libc。
FROM ubuntu:24.04

RUN mkdir -p /data
WORKDIR /app
COPY --from=build /build/minivectordb /app/minivectordb

VOLUME ["/data"]
ENTRYPOINT ["/app/minivectordb"]
CMD ["--data-dir", "/data"]
