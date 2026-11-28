#!/bin/sh
# Layar virtual -> VNC (hanya loopback) -> websockify :6090 untuk noVNC di FE.
# Chromium membuka DevTools di loopback :9222; socat meneruskannya ke :9333
# supaya bisa dijangkau middleware lewat jaringan Docker.
set -e
rm -f /tmp/.X99-lock /tmp/.X11-unix/X99

# layar tegak seukuran HP: halaman login (Google, ChatGPT) responsif, jadi di
# layar sempit tampil versi mobile (satu kolom, kolom email/password dan tombol
# besar) yang jauh lebih jelas saat ditampilkan di FE. Sengaja tanpa memalsukan
# user agent: cukup lebarnya, supaya login tidak dicurigai situsnya.
# Ukuran ini harus sama dengan LOGIN_SCREEN di FE (LoginDialog.tsx).
SCREEN_W="${LOGIN_SCREEN_W:-540}"
SCREEN_H="${LOGIN_SCREEN_H:-960}"

Xvfb "$DISPLAY" -screen 0 "${SCREEN_W}x${SCREEN_H}x24" -nolisten tcp &
sleep 1
x11vnc -display "$DISPLAY" -localhost -nopw -forever -shared -rfbport 5900 -quiet -bg -o /tmp/x11vnc.log
websockify 0.0.0.0:6090 127.0.0.1:5900 >/tmp/websockify.log 2>&1 &
socat TCP-LISTEN:9333,fork,reuseaddr TCP:127.0.0.1:9222 &

# profil sementara: setiap login memakai browser context terpisah yang
# langsung dibuang setelah sesinya diambil middleware
exec chromium \
  --user-data-dir=/tmp/profile \
  --remote-debugging-port=9222 \
  --no-sandbox --no-first-run --no-default-browser-check \
  --disable-dev-shm-usage --disable-gpu \
  --disable-background-networking --disable-sync \
  --window-position=0,0 --window-size="${SCREEN_W},${SCREEN_H}" \
  about:blank
