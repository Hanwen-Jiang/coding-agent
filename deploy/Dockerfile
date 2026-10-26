# syntax=docker/dockerfile:1.7
# coding-web 的镜像。go.mod 用 replace 指向并排检出的 Runtime 仓库（../agent-runtime 的 libs/go-core 与 proto/gen/go），
# 所以构建上下文是两个仓库的上一级目录：
#   docker build -f coding-agent/deploy/Dockerfile -t ghcr.io/<所有者>/coding-web:<版本> <上一级目录>
# 运行时要 git（克隆、看 diff、提交、推送）；以 10001 运行（与 Runtime 的 Worker、沙箱同一个用户，工作区目录归它所有），
# 根文件系统只读时 HOME 指向 /tmp。
ARG GO_IMAGE=golang:1.27-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY agent-runtime/libs/go-core/ agent-runtime/libs/go-core/
COPY agent-runtime/proto/gen/go/ agent-runtime/proto/gen/go/
COPY coding-agent/ coding-agent/
WORKDIR /src/coding-agent
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS=-mod=readonly
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/coding-web ./cmd/coding-web

FROM ${RUNTIME_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 coding && useradd --uid 10001 --gid 10001 --home-dir /tmp --no-create-home --shell /usr/sbin/nologin coding
COPY --from=build /out/coding-web /usr/local/bin/coding-web
ENV HOME=/tmp CODING_ADDR=0.0.0.0:8095
USER 10001:10001
EXPOSE 8095
ENTRYPOINT ["/usr/local/bin/coding-web"]
