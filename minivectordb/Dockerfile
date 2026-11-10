# syntax=docker/dockerfile:1

# 构建阶段：从源码编译，依赖全部在构建期安装（正式评测不允许联网）。
FROM ubuntu:24.04 AS build

RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential cmake \
 && rm -rf /var/lib/apt/lists/*

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
