# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# tahap 1: build binary Go
# ---------------------------------------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src

# dependensi diunduh dulu (layer terpisah), supaya tidak diunduh ulang bila
# hanya kode yang berubah
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal

# CGO_ENABLED=0: binary statis, jalan di alpine tanpa library C tambahan
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---------------------------------------------------------------------------
# tahap 2: image kecil untuk dijalankan
# ---------------------------------------------------------------------------
FROM alpine:3.21

# ca-certificates: koneksi HTTPS ke 9Router; tzdata: zona waktu Asia/Jakarta
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app && adduser -S -G app -H -h /app app \
    && mkdir -p /app/skills /app/logs /app/images /app/open_source_mcp \
    && chown -R app:app /app

WORKDIR /app
COPY --from=build /out/server /app/server

# berjalan sebagai user biasa, bukan root. Folder skills, logs, images, dan
# open_source_mcp (clone repo MCP dari GitHub)
# dibuat milik user ini supaya volume baru ikut bisa ditulis.
USER app

ENV APP_ENV=production \
    GIN_MODE=release \
    PORT=4000 \
    PATH_FILE_UPLOAD=/app/skills \
    LOG_DIR=/app/logs \
    IMAGE_PATH=/app/images \
    MCP_REPOS_PATH=/app/open_source_mcp \
    TZ=Asia/Jakarta

EXPOSE 4000

# start-period cukup panjang karena saat start ada migrasi database
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${PORT}/health" >/dev/null || exit 1

ENTRYPOINT ["/app/server"]
