# ---- 构建阶段 ----
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/xboxspeedup ./cmd/xboxspeedup

# ---- 运行阶段 ----
FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && \
    ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime
WORKDIR /app
COPY --from=build /out/xboxspeedup /app/xboxspeedup
# 内置域名表与 IP 快照作为首启播种源
COPY data /app/data

ENV XBOX_DATA_DIR=/data \
    XBOX_SEED_DIR=/app/data \
    TZ=Asia/Shanghai

VOLUME ["/data"]
EXPOSE 53/udp 53/tcp 80/tcp 443/tcp 8080/tcp 8443/tcp

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q -O /dev/null "${XBOX_HEALTH_URL:-http://127.0.0.1:8080/healthz}" || exit 1

ENTRYPOINT ["/app/xboxspeedup"]
