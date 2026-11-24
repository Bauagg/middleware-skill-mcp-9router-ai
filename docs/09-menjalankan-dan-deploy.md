# 9. Menjalankan, deploy, dan masalah umum

## Menjalankan di komputer lokal

```bash
# 1. siapkan env (Linux/macOS: cp)
copy .env.example .env
#    isi DB_*, REDIS_*, API_KEYS, JWT_SECRET

# 2. unduh library
go mod tidy

# 3. jalankan
go run ./cmd/server

# 4. cek
curl http://localhost:4000/health
```

Saat start, server:
1. membaca `.env` dan menolak jalan bila variabel wajib kosong;
2. menyambung ke PostgreSQL, lalu membuat/memperbarui tabel (`t_usage_logs`, `t_eval_runs`, `m_context_packs`, `t_memory_notes`, termasuk kolom pencarian teks);
3. menyambung ke Redis (berhenti bila gagal);
4. menyambung ke embedding bila `EMBEDDING_MODEL` diisi (berhenti bila gagal);
5. membaca folder `skills/` dan menyalakan watcher.

Untuk mengecek kode tanpa menjalankan server: `go build ./... && go vet ./...`.

## Redis DEV (Docker di VPS)

Redis utama di VPS (`host.docker.internal:6379`) hanya bisa dipakai dari container di VPS itu sendiri. Untuk pengembangan dari komputer lokal, ada Redis DEV terpisah di `docker/redis-dev/` (port **6380**):

```bash
# salin folder ke VPS
scp -r docker/redis-dev root@<IP_VPS>:/opt/redis-dev

# di VPS
cd /opt/redis-dev
cp .env.example .env          # isi REDIS_DEV_PASSWORD, atau salin .env lokal yang sudah berisi password
docker compose up -d
docker compose ps             # STATUS harus healthy
```

Lalu di `.env` proyek: `REDIS_ADDR=<IP_VPS>:6380`, `REDIS_PASSWORD` sama dengan `REDIS_DEV_PASSWORD`, dan `REDIS_TIMEOUT=2s`.

Isinya:
- `redis:7.4-alpine`, data permanen di volume `redis_dev_data`, restart otomatis, dan healthcheck.
- Batas memori 256 MB (key lama dibuang bila penuh).
- Perintah berbahaya (`FLUSHALL`, `FLUSHDB`, `CONFIG`, `DEBUG`, `MODULE`, `REPLICAOF`) dimatikan, karena port ini terbuka ke internet.

Bila password di VPS diganti, container harus dibuat ulang: `docker compose up -d --force-recreate`.

## Deploy dengan Docker

File yang dipakai:

| File | Isi |
|---|---|
| `Dockerfile` | Build 2 tahap: binary Go statis dibangun di `golang:1.25-alpine`, lalu dijalankan di `alpine:3.21` sebagai user biasa (bukan root). Ada healthcheck ke `/health`. |
| `docker-compose.yml` | Service `middleware`: membaca `.env`, volume untuk `skills/` dan `logs/`, `mcp.json` dipasang read-only, restart otomatis, dan batas ukuran log Docker |
| `.dockerignore` | Mencegah `.env`, `skills/`, `logs/`, dan file lain yang tidak perlu ikut masuk image |

### Menjalankan di VPS

```bash
# di VPS, di folder proyek (berisi Dockerfile, docker-compose.yml, dan .env)
docker compose up -d --build
docker compose ps                      # STATUS harus healthy
docker compose logs -f middleware
curl http://127.0.0.1:4000/health
```

Bila image dibuat oleh CI/CD dan disimpan di registry, isi `DOCKER_IMAGE` di `.env` dengan nama image itu, lalu deploy dengan:

```bash
docker compose pull && docker compose up -d
```

### `.env` untuk container di VPS

`docker-compose.yml` memakai `.env` di folder yang sama. Beberapa nilai **selalu ditimpa** compose supaya pengaturan development tidak terbawa: `APP_ENV=production`, `GIN_MODE=release`, `PORT=4000`, `PATH_FILE_UPLOAD=/app/skills`, `LOG_DIR=/app/logs`.

Yang perlu disesuaikan di `.env` VPS:

| Variabel | Nilai di VPS | Alasan |
|---|---|---|
| `REDIS_ADDR` | `host.docker.internal:6379` (Redis utama) | Container menjangkau VPS lewat `host.docker.internal` (sudah diatur di compose dengan `host-gateway`) |
| `REDIS_TIMEOUT` | `200ms` | Redis satu mesin, tidak perlu `2s` |
| `DB_HOST`, `DB_PORT` | alamat PostgreSQL, mis. `host.docker.internal` bila DB di VPS yang sama | |
| `DOCKER_PORT_BIND` | `127.0.0.1:4000` (bawaan) | Hanya bisa diakses dari VPS. Buka ke publik lewat NGINX. |

### Kenapa port bawaannya hanya 127.0.0.1

API `/api/*` **tidak memakai autentikasi**: siapa pun yang bisa mengaksesnya bisa meng-upload skill, mengubah context pack, dan mengonfirmasi atau menghapus catatan memori. Karena itu container hanya dibuka untuk VPS sendiri. Untuk diakses dari luar, pasang NGINX di depannya, dan sebaiknya hanya buka `/v1/*` ke publik, sedangkan `/api/*` dibatasi (misalnya per IP atau dengan basic auth di NGINX).

Bila NGINX juga berjalan di container, `127.0.0.1` tidak terjangkau dari container NGINX. Gunakan jaringan Docker bersama, atau ubah `DOCKER_PORT_BIND`.

### Data yang disimpan di volume

| Volume | Isi | Bila dihapus |
|---|---|---|
| `skills_data` -> `/app/skills` | Skill hasil upload | Semua skill hilang; harus di-upload ulang |
| `logs_data` -> `/app/logs` | File log aplikasi | Hanya file log yang hilang |
| `images_data` -> `/app/images` | Gambar hasil `generate_image` | Link gambar lama di riwayat chat jadi rusak |

Data penting lainnya (log pemakaian, eval, context pack, catatan memori) ada di PostgreSQL, bukan di container.

Mencadangkan skill:

```bash
docker run --rm -v middleware-skill-ai_skills_data:/s -v "$PWD":/b alpine tar czf /b/skills-backup.tgz -C /s .
```

### Mematikan dengan rapi (graceful shutdown)

Saat `docker stop` atau deploy ulang, server menerima SIGTERM lalu:
1. berhenti menerima request baru;
2. menunggu request yang sedang berjalan, termasuk chat streaming, maksimal 25 detik;
3. menulis sisa log pemakaian ke database;
4. keluar.

`stop_grace_period: 30s` di compose memberi waktu untuk langkah-langkah itu. Ekstraksi memori yang sedang berjalan di latar belakang tidak ditunggu. Pesan itu tidak akan diekstrak lagi, karena ekstraksi bersifat "bonus".

### Build manual tanpa Docker

```bash
go build -o bin/server ./cmd/server
```

`go.mod` **dan** `go.sum` wajib ada dan ikut di-commit, termasuk untuk build Docker.

## Masalah umum

| Gejala | Penyebab | Solusi |
|---|---|---|
| Semua file Go **merah** di VS Code (`could not import github.com/gin-gonic/gin`, `missing go.sum entry`) | File `go.sum` terhapus. Kodenya tidak salah. | Jalankan `go mod tidy`. **Jangan hapus `go.sum`**. |
| Server berhenti saat start: `REDIS_ADDR wajib diisi` atau `redis connection failed` | Redis kosong atau tidak terjangkau | Isi `REDIS_ADDR`, pastikan Redis hidup dan port terbuka |
| `WRONGPASS invalid username-password pair` | Password di `.env` tidak sama dengan password container Redis | Samakan `REDIS_PASSWORD` dengan `REDIS_DEV_PASSWORD` di VPS, lalu `docker compose up -d --force-recreate` |
| Cache selalu `off` padahal Redis hidup | Redis jauh dan `REDIS_TIMEOUT` terlalu kecil | Naikkan `REDIS_TIMEOUT` ke `2s` |
| Chat dijawab **401** | Header `Authorization` tidak berisi key 9Router yang benar. Middleware meneruskan key client apa adanya. | Kirim `Authorization: Bearer <key 9Router>` |
| Chat dijawab **403** dari client Python | Cloudflare di depan 9Router memblokir User-Agent `Python-urllib` | Pakai SDK OpenAI, atau isi User-Agent sendiri |
| SDK OpenAI gagal membaca jawaban (menerima SSE padahal menunggu JSON) | 9Router menjawab SSE bila field `stream` tidak dikirim | Sudah ditangani middleware (mengisi `"stream": false`). Bila masih terjadi, pastikan request lewat middleware, bukan langsung ke 9Router. |
| Model `kr/*` selalu error | "This version of Kiro is no longer supported" | Pakai `AI-Crypro` atau `ag/*` |
| Skill tidak pernah terpilih | Pesan dan deskripsi skill beda bahasa, atau threshold terlalu tinggi | Tambah `keywords` di `SKILL.md`, cek skor lewat `/api/skills/match` |
| Skill ada di disk tapi tidak muncul di `/api/skills` | Folder di luar `skills/`, atau `SKILL.md` di tingkat folder yang salah | Letakkan di `skills/<nama>/SKILL.md` |
| Model menolak menjawab saat skill tertentu aktif | Skill bergantung pada script atau data dari skill lain | Lihat [Skill](03-skill.md#skill-yang-tidak-cocok-untuk-middleware-contoh-founder-pricing) |
| `GET /api/usage-logs` error setelah menambah kolom baru | Kolom baru tanpa `default` membuat baris lama berisi NULL | Beri tag `not null;default:...` pada kolom baru (lihat [Struktur kode](02-struktur-kode.md#aturan-penamaan-tabel)) |
| Container `unhealthy` atau terus restart | Biasanya DB/Redis tidak terjangkau dari container, sehingga server berhenti saat start | `docker compose logs middleware`; untuk layanan di VPS yang sama pakai `host.docker.internal`, bukan `localhost` |
| Upload skill gagal `permission denied` di container | Volume lama dibuat oleh root | Hapus volume lalu buat ulang, atau `chown` isinya ke user `app` |
| Server berhenti saat start: `MCP_TOKEN_KEY tidak valid` | Ada server aktif di `mcp.json` tetapi kunci kosong/salah | Isi `MCP_TOKEN_KEY` dengan `openssl rand -hex 32` |
| `/connect` Figma: `registration failed with status 403` | Server MCP resmi Figma hanya menerima klien yang disetujui Figma | Lihat [MCP](10-mcp.md#figma-belum-bisa-dipakai-lewat-server-resminya) |
| Server MCP terus `needs_login` setelah login | Refresh token ditolak, atau `MCP_TOKEN_KEY` diganti | Login ulang lewat `connect_url` |
| Halaman login MCP kembali ke alamat yang tidak bisa dibuka | `BASE_URL` bukan alamat yang bisa dijangkau browser | Isi `BASE_URL` dengan alamat publik (lewat NGINX), lalu `/connect` lagi |
| Gambar di jawaban chat tampil rusak | `BASE_URL` tidak bisa dibuka aplikasi chat, atau `/images/*` tidak dibuka di NGINX | Isi `BASE_URL` dengan alamat publik dan buka `/images/*` |
| Minta gambar tapi model hanya menjawab teks | Akun 9Router user belum punya model gambar (`GET /v1/models/image` kosong), atau pesan tidak memuat kata seperti "gambar"/"logo"/"ilustrasi" | Tambahkan provider gambar di dashboard 9Router akun itu; lihat [Gambar lewat chat](11-gambar.md) |
| Semua request dijawab **401** `header X-API-Key tidak ada atau salah` | Client belum mengirim `X-API-Key`, atau nilainya tidak ada di `API_KEYS` | Kirim header `X-API-Key` berisi salah satu key di `.env` |
| Server berhenti saat start: `API_KEYS wajib diisi` | `API_KEYS` kosong atau key kurang dari 32 karakter | Isi dengan `openssl rand -hex 32` |
| Aplikasi web gagal dengan error CORS | Header kustom tidak diizinkan | Sudah diizinkan: `Authorization`, `X-API-Key`, `X-Router-URL`, `X-Router-Key`, `X-User-Id`, `X-Conversation-Id`. Pastikan memakai versi terbaru. |
| Chat dijawab **400** `header X-Router-URL wajib berisi alamat 9Router` | Client belum mengirim alamat 9Router user, atau formatnya salah | Kirim header `X-Router-URL`, mis. `https://domain-9router/v1` |
| Chat dijawab **502** dan log berisi `mengarah ke jaringan internal` | Alamat 9Router menunjuk localhost/IP privat | Pakai alamat publik 9Router; untuk pengembangan lokal set `ROUTER_ALLOW_PRIVATE_NETWORK=true` |
| Chat dijawab **401** `header X-Router-Key wajib diisi` | Client belum mengirim key 9Router milik user | Kirim header `X-Router-Key` berisi key 9Router user |
| Chat dijawab **400** `field model wajib diisi` | Body chat tidak membawa `model` | Kirim `model` (mis. `AI-Crypro`) |
| Catatan memori tidak pernah muncul sebagai `proposed` | `MEMORY_EXTRACT_ENABLED=false`, atau pesan tidak berisi kata sinyal hasil | Set `MEMORY_EXTRACT_ENABLED=true`; cek log aplikasi `ekstraksi memori` |
