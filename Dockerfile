# MiniVectorDB: build from source, run as a foreground stdin/stdout process.
# Only ubuntu:24.04 (the base image from the task statement) is pulled. The Go
# toolchain comes from the Ubuntu archive (golang-1.22-go 1.22.2-2ubuntu0.5), and
# the code uses only the Go standard library, so the build needs no Go proxy.
#
# Version locking: the base image is pinned by digest, so the build always starts
# from the same bits; the Go package is pinned to an exact archive version and
# falls back to the archive candidate only if the pool has moved on (an Ubuntu
# security bump must not turn into a broken submission).

FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852 AS build
ENV DEBIAN_FRONTEND=noninteractive
# Fall back to a domestic mirror if the default archive is unreachable.
RUN (apt-get update || (sed -i 's#http://\(archive\|security\).ubuntu.com#http://mirrors.aliyun.com#g' \
        /etc/apt/sources.list.d/ubuntu.sources && apt-get update)) && \
    { apt-get install -y --no-install-recommends golang-1.22-go=1.22.2-2ubuntu0.5 || \
      apt-get install -y --no-install-recommends golang-1.22-go ; } && \
    /usr/lib/go-1.22/bin/go version | grep -q 'go1\.22\.' && \
    rm -rf /var/lib/apt/lists/*
ENV PATH=/usr/lib/go-1.22/bin:$PATH CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    GOPROXY=off GOTOOLCHAIN=local GOFLAGS=-trimpath
WORKDIR /src
COPY minivectordb/ ./
RUN go build -ldflags="-s -w" -o /out/minivectordb ./cmd/minivectordb

FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852
WORKDIR /app
COPY --from=build /out/minivectordb /app/minivectordb
RUN chmod 755 /app/minivectordb && mkdir -p /data
VOLUME ["/data"]
ENTRYPOINT ["/app/minivectordb"]
CMD ["--data-dir", "/data"]
