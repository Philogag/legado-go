# syntax=docker/dockerfile:1.7
# ---------------------------------------------------------------------------
# legado-go 多阶段构建
#   builder: golang:1.26-alpine（纯静态编译 CGO_ENABLED=0）
#   runtime: distroless static（无 shell、非 root、自带 CA 证书）
# 产物：/legado-server（单二进制，含 REST + MCP 双入口）
# ---------------------------------------------------------------------------
# 关键：用 --platform=$BUILDPLATFORM 让编译器跑在本机架构上，
# 再用 TARGETOS/TARGETARCH 交叉编译到目标平台。
# 否则 buildx 会把整个构建阶段放进 qemu 模拟，慢且易出奇怪问题。
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /src

# push-to-ghcr 会注入这两个 build-arg（见 .github/workflows/docker-publish.yml）
ARG BUILD_DATE
ARG GITHUB_SHA
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

ENV CGO_ENABLED=0 \
    GOFLAGS=-trimpath \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH}

# 先只拷贝依赖清单，命中 Docker 层缓存时不必重复下载
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags "-s -w -X main.buildCommit=${GITHUB_SHA} -X main.buildDate=${BUILD_DATE} -X main.buildVersion=${VERSION}" \
      -o /out/legado-server ./cmd/server

# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

ARG BUILD_DATE
ARG GITHUB_SHA

LABEL org.opencontainers.image.title="legado-go" \
      org.opencontainers.image.description="Legado 书源引擎微服务（REST + MCP + TTL 实例管理）" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.revision="${GITHUB_SHA}"

COPY --from=builder /out/legado-server /legado-server

# distroless 的 nonroot 用户 uid/gid 65532
USER nonroot:nonroot

# 服务端口（与 ADDR 默认值 :8080 一致）
EXPOSE 8080

# distroless 没有 shell，健康检查必须用 exec 形式
# 二进制内置 -healthcheck 模式（非 2xx 退出码 1），见 cmd/server/main.go
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/legado-server", "-healthcheck", "http://127.0.0.1:8080/readyz"]

ENTRYPOINT ["/legado-server"]
