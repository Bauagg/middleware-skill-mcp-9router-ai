# Image runner untuk server MCP yang dipasang dari GitHub lewat chat.
# Satu image untuk semua repo: middleware membuat container dari image ini,
# lalu container meng-clone repo pada commit yang disetujui, menjalankan
# perintah install, dan menjalankan servernya (stdio dibungkus supergateway
# menjadi Streamable HTTP di port 8000).
#
# Dibangun otomatis oleh `docker compose up -d --build` (service mcp-runner).

FROM node:22-bookworm-slim

# git untuk clone, Python + build tools untuk repo Python dan modul native Node
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates python3 python3-venv python3-pip build-essential \
 && rm -rf /var/lib/apt/lists/*

# uv: pengelola paket Python yang cepat (uv / uvx)
COPY --from=ghcr.io/astral-sh/uv:0.13.9 /uv /uvx /usr/local/bin/

# pnpm/yarn lewat corepack; supergateway untuk mengubah MCP stdio menjadi HTTP
RUN corepack enable \
 && npm install -g supergateway@4.2.4 \
 && npm cache clean --force

# tidak berjalan sebagai root; /app adalah folder kerja repo
RUN useradd -m -u 10001 mcp && mkdir /app && chown mcp:mcp /app
USER mcp
WORKDIR /app
ENV NPM_CONFIG_UPDATE_NOTIFIER=false \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

EXPOSE 8000
