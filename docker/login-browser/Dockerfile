# Browser untuk login akun AI (Google, ChatGPT, ...) dari halaman "Akun AI" di FE.
# Chromium berjalan dengan layar virtual (Xvfb), bukan headless: halaman login
# Google dan ChatGPT menganggap Chrome headless sebagai bot. Layarnya dikirim lewat noVNC
# (websockify, port 6090) dan dikendalikan middleware lewat DevTools (port 9333).
# Kedua port tidak boleh dibuka ke publik: DevTools = kendali penuh browser.
#
# Dibangun otomatis oleh `docker compose up -d --build` (service login-browser).

FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      chromium xvfb x11vnc websockify socat \
      fonts-liberation fonts-noto-color-emoji ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -u 1000 browser

COPY entrypoint.sh /entrypoint.sh
USER browser
ENV DISPLAY=:99
EXPOSE 9333 6090
ENTRYPOINT ["/bin/sh", "/entrypoint.sh"]
