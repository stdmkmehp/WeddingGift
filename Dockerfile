# syntax=docker/dockerfile:1

# ---- 构建阶段 ----
FROM golang:1.27-alpine AS build

WORKDIR /src

# 先复制依赖清单以利用缓存（M3 起有外部依赖，go.sum 必然存在；保留可选语法兼容早期分支）。
COPY go.mod ./
COPY go.su[m] ./
RUN go mod download

# 复制源码并构建静态二进制（CGO 关闭，可运行于 scratch）。
COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/weddinggift/wedding/internal/buildinfo.Version=${VERSION} \
      -X github.com/weddinggift/wedding/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/weddinggift/wedding/internal/buildinfo.BuildTime=${BUILD_TIME}" \
    -o /out/wedding ./cmd/wedding

# ---- 运行阶段 ----
# 使用 scratch 精简基底：镜像内无 shell、curl、wget；健康检查靠 `wedding healthz-check`。
FROM scratch AS runtime

# 提供非 root 用户与 CA 证书（供后续里程碑的 HTTPS 兑换链接校验等使用）。
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /etc/passwd /etc/passwd

COPY --from=build /out/wedding /app/wedding

# 以非 root 运行（65534 = nobody，alpine 的 /etc/passwd 中存在）。
USER 65534:65534

EXPOSE 3000

ENV WEDDING_LISTEN_ADDR=":3000"

# Docker 层面的健康检查：调用应用自带子命令，不依赖镜像内 curl/wget。
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/app/wedding", "healthz-check"]

ENTRYPOINT ["/app/wedding"]
CMD ["serve"]
