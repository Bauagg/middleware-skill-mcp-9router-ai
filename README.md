# middleware-skill-ai

Middleware **Go + Gin + GORM (PostgreSQL) + Redis** yang berdiri di depan **9Router milik user**. Setiap chat dibekali skill, context pack, dan catatan memori, lalu bisa memakai tool: MCP (Canva, Figma, dan server lain), MCP yang dipasang dari GitHub (berjalan di container Docker terpisah), pembuat gambar, pencarian web, buat skill, dan impor skill dari GitHub. Konvensi arsitektur mengikuti template [`Bauagg/template-express-ts-backend`](https://github.com/Bauagg/template-express-ts-backend) yang diportalkan ke gaya idiomatik Go.

> **Dokumentasi lengkap** (cara kerja, struktur kode, skill, memori, API, konfigurasi, deploy, MCP, gambar, akun, pasang MCP dari GitHub, dan solusi error umum) ada di folder [`docs/`](docs/README.md).

## Akses dan Identitas (ringkas)

Ada **dua cara masuk**, keduanya bisa dipakai bersamaan:

| Cara | Untuk | Header | `user_id` | Role |
|---|---|---|---|---|
| Aplikasi pihak ketiga | aplikasi yang sudah punya user sendiri (mis. Luma) | `X-API-Key` (dari `API_KEYS`) + `X-User-Id` | isi `X-User-Id` | selalu `user` |
| Akun middleware | user yang register/login di middleware (web middleware AI) | `Authorization: Bearer <access_token>` | `id` di `m_users` (header `X-User-Id` diabaikan) | `user` atau `admin` |

Chat (`/v1/*`) juga wajib membawa **9Router milik user**: `X-Router-URL` (alamat, 400 bila tidak ada), `X-Router-Key` (key, 401 bila tidak ada), dan field `model` (400 bila tidak ada). Middleware **tidak punya** alamat, key, atau model 9Router sendiri di `.env`.

- **Admin** = pemilik aplikasi (dibuat dari `ADMIN_EMAIL`/`ADMIN_PASSWORD`). Admin melihat data **semua** user di `/api/*`, dan server MCP yang dibuat admin berlaku untuk semua user.
- **User biasa**, termasuk semua user dari pihak ketiga, hanya melihat datanya sendiri.
- **Skill selalu global** untuk semua user.
- Endpoint terbuka tanpa key/token: `/`, `/health`, `/images/...`, `POST /api/auth/register|login|refresh`, alur login MCP, dan form data rahasia MCP (keduanya lewat link bertanda tangan).

Detail: [`docs/07-api.md`](docs/07-api.md) dan [`docs/12-akun-dan-login.md`](docs/12-akun-dan-login.md).

## Tech Stack

| Kebutuhan | Pustaka |
|---|---|
| Framework HTTP | **Gin** |
| ORM / DB | **GORM + PostgreSQL** (driver `pgx`) |
| CORS | **gin-contrib/cors** |
| Logger | **uber-go/zap** (file + console) |
| Validasi request | **go-playground/validator** (bawaan Gin) |
| Config | **godotenv** |
| Cache | **Redis** (`redis/go-redis`) |
| Token login | **golang-jwt/jwt v5** (HS256) + **bcrypt** (`golang.org/x/crypto`) |
| Klien MCP | **modelcontextprotocol/go-sdk** (Streamable HTTP) + OAuth (`golang.org/x/oauth2`) |
| Container MCP dari GitHub | Docker Engine API (tanpa SDK) lewat **docker-socket-proxy**, runner berisi **supergateway** |
| Pencarian web | **SearXNG** (self-host, service di docker-compose) |

## Struktur Folder

```
middleware-skill-ai/
├── cmd/server/main.go            # Entry point (config -> logger -> DB -> token -> middleware -> router -> run)
├── internal/
│   ├── config/config.go          # Muat .env -> struct Config
│   ├── logger/logger.go          # zap: logs/combined.log + logs/error.log + console
│   ├── database/database.go      # Koneksi GORM, AutoMigrate, ByUser (filter per user; "" = admin lihat semua)
│   ├── middleware/               # auth (token login / X-API-Key), cors, http-logger, recovery (panic->500)
│   ├── router/router.go          # Wiring terpusat semua service dan route
│   ├── services/                 # satu folder per domain (controller, service, repository, route, dto, model)
│   │   ├── auth/                 # register, login, refresh token, akun admin dari .env (m_users)
│   │   ├── skillstore/           # upload, daftar, export, Render skill; tool create_skill & import_skills_from_github
│   │   ├── skillindex/           # index skill di memori, pemilih skill (BM25), skill aktif percakapan, watcher
│   │   ├── skillcache/           # cache Redis: skill aktif per percakapan + hasil seleksi
│   │   ├── skillfallback/        # pemilih cadangan: model chat user memilih skill saat skor ambigu
│   │   ├── proxy/                # proxy /v1/* + penyisipan skill/context pack/catatan + putaran tool
│   │   ├── eval/                 # eval skill: EVAL.yaml -> jawaban model -> skor hakim -> t_eval_runs
│   │   ├── contextpack/          # context pack per user (m_context_packs)
│   │   ├── memorynote/           # catatan memori per user (t_memory_notes): ekstraksi, konfirmasi, pencarian
│   │   ├── imagegen/             # tool generate_image: gambar lewat chat, simpan di images/ + t_generated_images
│   │   ├── websearch/            # tool web_search lewat SearXNG + GET /api/web-search
│   │   ├── mcp/                  # MCP: mcp.json, server admin (global) & pribadi, login OAuth per user, tool kelola lewat chat
│   │   ├── mcpinstall/           # pasang MCP dari GitHub: analisis, persetujuan admin, form rahasia, container Docker
│   │   └── usagelog/             # log pemakaian (t_usage_logs) + GET /api/usage-logs
│   ├── clients/                  # klien layanan pihak ketiga, satu file per layanan
│   │   ├── 9router.go            # proxy ke 9Router milik user (alamat & key dari context), pengamatan token
│   │   ├── routerurl.go          # validasi X-Router-URL + transport yang menolak alamat jaringan internal
│   │   ├── images.go             # buat gambar (POST /images/generations) + daftar model gambar akun user
│   │   ├── embedding.go          # klien embedding umum (POST /embeddings)
│   │   ├── mcp.go                # klien server MCP (Streamable HTTP, go-sdk): daftar & panggil tool
│   │   ├── mcpauth.go            # discovery OAuth server MCP + pendaftaran klien otomatis (DCR)
│   │   ├── github.go             # unduh zip repo GitHub + SHA commit
│   │   ├── docker.go             # Docker Engine API: buat/jalankan/hentikan/hapus container MCP
│   │   ├── websearch.go          # klien SearXNG
│   │   └── redis.go              # koneksi Redis (timeout pendek, gagal = cache miss)
│   └── pkg/                      # pustaka bersama, tidak tahu soal domain
│       ├── response/, apperror/  # amplop {status,message,data,errors}, AppError + Handle
│       ├── jwtauth/              # access & refresh token (JWT HS256)
│       ├── userctx/              # user_id, role, scope admin/user, key/URL/model 9Router di context
│       ├── frontmatter/, bm25/   # parser SKILL.md, tokenizer + skor BM25
│       ├── openai/, tokens/      # baca usage OpenAI (JSON/SSE), perkiraan token
│       ├── skillclean/, modelbudget/  # pembersih isi skill, budget token per model
│       ├── uuidparam/, vector/   # validasi :id UUID, kemiripan kosinus
│       ├── secretbox/            # enkripsi AES-256-GCM (token login MCP, data rahasia MCP dari GitHub)
│       └── llmtool/              # bentuk tool untuk model + hasilnya
├── docker/mcp-runner/Dockerfile  # image runner MCP dari GitHub (git, Node 22, pnpm, Python, uv, supergateway)
├── searxng/settings.yml          # konfigurasi SearXNG
├── docs/                         # dokumentasi lengkap (mulai dari docs/README.md)
├── Dockerfile                    # image middleware (build 2 tahap, user non-root, healthcheck)
├── docker-compose.yml            # middleware + searxng + docker-proxy + mcp-runner (lihat docs/09)
├── mcp.json                      # server MCP bawaan (lihat docs/10-mcp.md)
├── skills/                       # folder skill (dibuat otomatis, tidak di-commit)
├── images/                       # gambar hasil generate (dibuat otomatis, tidak di-commit)
├── open_source_mcp/              # clone repo MCP dari GitHub untuk diperiksa (dibuat otomatis, tidak di-commit)
├── logs/                         # output log (dibuat otomatis)
├── .env.example
└── go.mod
```

## Menjalankan

```bash
# 1. Siapkan env
copy .env.example .env      # Linux/macOS: cp .env.example .env
#    isi DB_* (PostgreSQL), REDIS_ADDR (Redis), API_KEYS, JWT_SECRET; semuanya wajib. Alamat, key, dan model 9Router dikirim tiap user, bukan di .env

# 2. Unduh dependensi
go mod tidy

# 3. Jalankan (development)
go run ./cmd/server

# Build binary (lokal; build rilis lewat Docker di CI/CD)
go build -o bin/server ./cmd/server

# Docker (di VPS)
docker compose up -d --build
```

Server default di `http://localhost:4000`. Saat start, koneksi PostgreSQL dan Redis wajib
berhasil (bila tidak, server berhenti dengan pesan error), lalu **semua tabel dibuat/diperbarui
otomatis** (AutoMigrate): `t_usage_logs`, `t_eval_runs`, `m_context_packs`, `t_memory_notes`,
`m_mcp_credentials`, `m_mcp_servers`, `t_generated_images`, `m_users`, `m_mcp_installs`, dan
`m_mcp_install_instances`. Akun admin dibuat dari `ADMIN_EMAIL`/`ADMIN_PASSWORD` bila diisi.

Variabel wajib di `.env`: `DB_*`, `REDIS_ADDR`, `API_KEYS` (secret aplikasi pihak ketiga, min. 32
karakter), dan `JWT_SECRET` (min. 32 karakter). `MCP_TOKEN_KEY` dibutuhkan untuk login MCP dan
pasang MCP dari GitHub. Daftar lengkap: [`docs/08-konfigurasi.md`](docs/08-konfigurasi.md).

### Docker Compose (VPS)

`docker compose up -d --build` menjalankan:

| Service | Fungsi |
|---|---|
| `middleware` | aplikasi ini (port `127.0.0.1:4000`, buka ke publik lewat NGINX) |
| `searxng` | mesin pencari untuk tool `web_search` |
| `docker-proxy` | proxy Docker API yang hanya membuka urusan container, untuk pasang MCP dari GitHub |
| `mcp-runner` | hanya membangun image runner MCP, lalu selesai |

Network `middleware_mcp_net` untuk container MCP, `docker_api` (internal) untuk middleware dan proxy saja. Volume: `skills_data`, `logs_data`, `images_data`, `mcp_repos_data`. Detail: [`docs/09-menjalankan-dan-deploy.md`](docs/09-menjalankan-dan-deploy.md).

## Redis DEV

Redis utama di VPS (`host.docker.internal:6379`) hanya bisa dipakai dari container di VPS. Untuk pengembangan dari komputer lokal dipakai Redis DEV terpisah yang berjalan sebagai container `redis-dev` di VPS (dikelola langsung di VPS, folder-nya tidak lagi ada di repo ini).

Di `.env` lokal: `REDIS_ADDR` = alamat Redis DEV di VPS, `REDIS_PASSWORD` = password dari env container `redis-dev`, dan `REDIS_TIMEOUT=2s` (Redis di VPS berjarak 0,3-0,7 detik dari komputer lokal; bawaan 200 ms akan selalu timeout). Bila port-nya tidak bisa dijangkau, buka port itu di firewall VPS / security group penyedia cloud.

## Endpoint (saat ini)

Semua `/api/*` data per user butuh identitas (token login, atau `X-API-Key` + `X-User-Id`). Admin melihat data semua user; user biasa hanya miliknya.

| Method | URL | Keterangan |
|---|---|---|
| GET | `/` | `Server is running` |
| GET | `/health` | `{ status: ok }` |
| GET | `/api/ping` | `{ message: pong }` |
| POST | `/api/auth/register` | Daftar akun (role `user`) + token. Terbuka |
| POST | `/api/auth/login` | Login + token. Terbuka |
| POST | `/api/auth/refresh` | Tukar refresh token dengan pasangan token baru. Terbuka |
| GET | `/api/auth/me` | Akun yang sedang login (butuh access token) |
| GET | `/api/skills` | Daftar skill yang terpasang |
| POST | `/api/skills/upload` | Upload skill dalam bentuk zip |
| GET | `/api/skills/export?names=a,b` | Download zip skill yang terpasang (tanpa `names` = semua) |
| GET | `/api/skills/match?q=...&limit=5&fallback=false` | Skill apa yang akan terpilih untuk sebuah pesan, skor kandidat, dan apakah skornya ambigu (`fallback=true` ikut menjalankan pemilih cadangan) |
| GET | `/api/skills/preview/:name?model=...&budget=...` | Teks skill persis seperti yang akan disisipkan (sudah dibersihkan dan sesuai budget) |
| POST | `/v1/chat/completions` | Proxy ke 9Router; skill yang relevan, context pack aktif, dan catatan memori yang cocok disisipkan ke system prompt |
| ANY | `/v1/*` | Proxy polos ke 9Router (mis. `/v1/models`) |
| GET | `/api/usage-logs?page=&limit=&model=&skill=&user_id=` | Daftar log pemakaian, terbaru lebih dulu (user: miliknya; admin: semua, bisa difilter `user_id`) |
| GET | `/api/usage-logs/summary?from=&to=` | Ringkasan per skill (request, rata-rata token tambahan, token, latensi), hit/miss cache, varian skill, dan penyisipan memori |
| POST | `/api/evals/runs` | Mulai eval skill (berjalan di latar belakang), body `{"skill","model","judge_model","variants","limit"}` + header `X-Router-URL`/`X-Router-Key` |
| GET | `/api/evals/runs?skill=&limit=` | Daftar eval terbaru |
| GET | `/api/evals/runs/:id?detail=true` | Hasil eval per varian beserta kesimpulannya |
| GET | `/api/mcp/servers` | Server MCP yang terlihat user (bawaan, dari admin, pribadi) + status dan link login (`global`, `shared_login`) |
| POST | `/api/mcp/servers` | Tambah server: dari admin = untuk semua user (`shared_login`), dari user = pribadi |
| DELETE | `/api/mcp/servers/:name` | Hapus server pribadi (server admin hanya oleh admin) |
| GET | `/api/mcp/admin/servers` | **Admin:** server tambahan dan status login semua user |
| POST | `/api/mcp/installs/analyze` | Pasang MCP dari GitHub: unduh & periksa repo (tanpa dijalankan) |
| POST / GET | `/api/mcp/installs` | Ajukan pemasangan / daftar pemasangan (milik sendiri + dari admin) |
| GET / DELETE | `/api/mcp/installs/:id` | Detail / hapus pemasangan |
| POST | `/api/mcp/installs/:id/approve` · `/reject` | **Admin:** setujui / tolak pengajuan user |
| POST | `/api/mcp/installs/:id/start` · `/stop` | Jalankan / hentikan container |
| GET | `/api/mcp/installs/:id/logs` | Log container |
| GET / POST | `/api/mcp/installs/:id/env?u=&exp=&sig=` | Form data rahasia (link dari chat) |
| GET | `/api/mcp/servers/:name/connect` | Login OAuth server MCP (buka di browser) |
| POST | `/api/mcp/servers/:name/refresh` | Ambil ulang daftar tool server MCP |
| DELETE | `/api/mcp/servers/:name/connection` | Hapus login server MCP |
| GET | `/api/mcp/oauth/callback` | Tujuan balik setelah login OAuth (dipanggil browser) |
| GET | `/api/mcp/tools?q=` | Tool MCP yang terhubung dan yang akan terpilih untuk sebuah pesan |
| GET | `/images/:file` | Gambar hasil `generate_image` (link yang muncul di jawaban chat) |
| GET | `/api/images?q=&page=&limit=` | Daftar gambar yang pernah dibuat (URL, prompt, alt, model) dari tabel `t_generated_images` |
| GET | `/api/images/:id` | Detail satu gambar |
| GET | `/api/web-search?q=` | Cek pencarian web lewat SearXNG |
| GET | `/api/context-packs` | Daftar context pack |
| POST | `/api/context-packs` | Buat context pack, body `{"name","content","active"}` |
| GET / PUT / DELETE | `/api/context-packs/:id` | Lihat, ubah (field yang dikirim saja), atau hapus context pack |
| GET | `/api/memory-notes?status=&q=&page=&limit=` | Daftar/cari catatan memori (`status`: proposed, active, corrected, rejected, deleted) |
| GET | `/api/memory-notes/match?q=...` | Catatan apa yang akan disisipkan untuk sebuah pesan, beserta teks bloknya |
| POST | `/api/memory-notes` | Tulis catatan manual (langsung aktif) |
| GET / PUT / DELETE | `/api/memory-notes/:id` | Lihat, koreksi, atau hapus catatan |
| POST | `/api/memory-notes/:id/confirm` | Konfirmasi catatan usulan (proposed/rejected -> active) |
| POST | `/api/memory-notes/:id/reject` | Tolak catatan; catatan yang sama tidak diusulkan lagi |

## Log Pemakaian

Setiap `POST /v1/chat/completions` dicatat ke tabel `t_usage_logs` di PostgreSQL (dibuat otomatis saat start, butuh PostgreSQL 13+ untuk `gen_random_uuid()`). Aturan penamaan: tabel transaksi diawali `t_`. Tujuannya mengukur berapa token tambahan yang dipakai skill, seberapa sering skill terpilih, dan seberapa lama request.

| Kolom | Isi |
|---|---|
| `model`, `path`, `stream` | dari request |
| `skill_name`, `skill_score` | skill yang disisipkan dan skor BM25-nya (NULL bila tanpa skill) |
| `extra_tokens_est` | **perkiraan** token tambahan dari skill (karakter / 4), bukan hitungan tokenizer |
| `skill_variant`, `budget_tokens` | `full` / `short` / `truncated`, dan batas token skill untuk model itu (0 = tanpa batas) |
| `selection_cache`, `session_cache` | `hit` / `miss` / `off` (sesi juga `switch`); kosong bila tidak berlaku |
| `selection_method`, `fallback_tokens` | `bm25` / `embedding` / `fallback`, dan token yang dipakai pemilih cadangan (0 bila tidak dipanggil) |
| `context_pack_tokens_est` | perkiraan token context pack yang disisipkan (0 bila tidak ada) |
| `memory_notes`, `memory_note_ids`, `memory_tokens_est` | jumlah catatan memori yang disisipkan, id-nya (dipisah koma), dan perkiraan tokennya |
| `prompt_tokens`, `completion_tokens`, `cached_tokens` | dilaporkan 9Router/provider; **NULL bila tidak dilaporkan** (berbeda dengan 0) |
| `status`, `latency_ms`, `aborted` | status dari 9Router, total waktu request, dan apakah client memutus koneksi |

Catatan:
- Pencatatan berjalan di latar belakang (antrean, ditulis per 100 baris atau tiap 2 detik) dan tidak pernah menunda request. Bila antrean penuh, catatan dibuang dan dicatat di log aplikasi. Catatan yang masih di antrean saat proses dihentikan paksa bisa hilang.
- Pada **streaming**, provider hanya mengirim `usage` bila client menyertakan `"stream_options": {"include_usage": true}` di request. Tanpa itu, kolom token bernilai NULL. Middleware sengaja tidak menambahkannya sendiri supaya respons ke client tidak berubah.
- Nama field token cache berbeda antar provider; yang dibaca: `prompt_tokens_details.cached_tokens` (OpenAI), `prompt_cache_hit_tokens` (DeepSeek), dan `cache_read_input_tokens` (gaya Anthropic).
- Header `Accept-Encoding` dari client tidak diteruskan ke 9Router supaya token bisa dibaca; respons ke client tidak terkompresi.

## Proxy ke 9Router

Arahkan client OpenAI-compatible ke middleware ini sebagai pengganti 9Router; selebihnya sama. Alamat 9Router dikirim client di header `X-Router-URL` (mis. `https://host/v1`); path `/v1/xyz` dari client diteruskan ke `<X-Router-URL>/xyz`.

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "X-API-Key: <API_KEYS>" -H "X-Router-URL: https://<domain-9router>/v1" -H "X-Router-Key: <KEY 9ROUTER MILIK USER>" -H "X-User-Id: user-123" \
  -H "Content-Type: application/json" \
  -d '{"model":"AI-Crypro","messages":[{"role":"user","content":"berapa harga yang pas untuk produk saya"}]}'
```

Untuk `POST /v1/chat/completions`:
1. Ambil pesan user terakhir (teks biasa atau array bagian; gambar diabaikan).
2. Pilih skill (BM25, embedding bila aktif, dan pemilih cadangan bila skor ambigu). Bila tidak ada yang cocok, request diteruskan tanpa skill (tambahan token nol).
3. Isi `SKILL.md` (tanpa frontmatter) dibungkus `<skill name="...">...</skill>`, lalu disusul context pack aktif (`<context_pack>`) dan catatan memori yang cocok (`<memory_notes>`). Urutannya selalu skill -> context pack -> catatan, dan semuanya ditaruh di **awal** system prompt. Bila belum ada pesan system, dibuat satu di paling depan. Context pack dan catatan tetap disisipkan walau tidak ada skill yang terpilih.
4. Request diteruskan ke 9Router, respons (termasuk SSE) dialirkan balik tanpa ditampung.

Catatan:
- Middleware adalah alat di depan 9Router milik user: setiap user mengirim **alamat 9Router miliknya** di header `X-Router-URL` (wajib, 400 bila tidak ada atau tidak valid), **key 9Router miliknya** di header `X-Router-Key` (wajib untuk semua `/v1/*`, 401 bila tidak ada) dan **model miliknya** di field `model` (wajib, 400 bila tidak ada). Semua panggilan ke 9Router selama chat itu (jawaban, tool, gambar, pemilih cadangan, ekstraksi memori) memakai key dan model user tersebut. Middleware tidak punya alamat, key, atau model 9Router sendiri di `.env`. Alamat yang mengarah ke jaringan internal (localhost, IP privat, metadata cloud) ditolak, kecuali `ROUTER_ALLOW_PRIVATE_NETWORK=true`. Header `Authorization` dari client diabaikan; akses ke middleware dijaga header `X-API-Key`.
- Hanya field `messages` yang diubah, ditambah `"stream": false` bila client tidak mengirim field `stream`. Standar OpenAI menetapkan bawaannya false, tetapi 9Router menjawab SSE bila field itu tidak ada, sehingga SDK OpenAI yang menunggu JSON akan gagal. Field lain di body, termasuk yang tidak dikenal, dibiarkan.
- Header client (termasuk `User-Agent`) diteruskan apa adanya, kecuali `X-API-Key`, `X-Router-URL`, `X-Router-Key`, dan `X-User-Id` yang dibuang. 9Router berada di belakang Cloudflare yang memblokir User-Agent bawaan Python (`Python-urllib`) dengan 403; client Python sebaiknya memakai SDK OpenAI atau mengisi User-Agent sendiri.
- Respons diberi header `X-Skill: <nama>` bila ada skill yang disisipkan.
- Gagal terbuka: bila langkah skill gagal (body bukan JSON, file skill hilang), request tetap diteruskan tanpa skill dan masalahnya dicatat di log.
- Body maksimal 20 MB (413 bila lebih). Error dari proxy sendiri (413, 502, 504) berbentuk `{"error":{"message","type"}}` agar terbaca SDK OpenAI.
- Skill yang isinya memerintahkan menjalankan script (mis. `python3 ...`) tetap disisipkan apa adanya; model tidak bisa menjalankannya. Pembersihan isi ada di Tahap 2.

## Pemilihan Skill (BM25)

Skill dipilih tanpa LLM: BM25 pada gabungan `name` (bobot 3), `keywords` (bobot 2), dan `description`. Hanya satu skill teratas yang dipilih, dan hanya bila skornya mencapai `SKILL_MIN_SCORE` (default `2.0`). Sapaan dan ucapan terima kasih ("halo", "terima kasih") tidak menghasilkan skor, jadi tidak mendapat skill.

```bash
curl "http://localhost:4000/api/skills/match?q=berapa+harga+yang+pas+untuk+produk+saya" -H "X-API-Key: <API_KEYS>"
```

```json
{ "status": "success", "data": { "query": "...", "threshold": 2, "selected": "founder-pricing",
  "candidates": [ { "name": "founder-pricing", "description": "...", "score": 3.76 } ] } }
```

`selected` bernilai `null` bila skor tertinggi di bawah threshold. Pakai endpoint ini untuk menyetel `SKILL_MIN_SCORE`.

**Keywords (opsional).** BM25 hanya mencocokkan kata yang sama persis. Bila deskripsi skill berbahasa Inggris tetapi pesan user berbahasa Indonesia, tambahkan padanannya di frontmatter `SKILL.md`:

```yaml
---
name: founder-pricing
description: Finds the price buyers will actually pay...
keywords: harga, menentukan harga, tarif
---
```

`keywords` boleh berupa teks dipisah koma atau daftar YAML. Contoh hasil pada 11 skill `founder-*`: pesan "berapa harga yang pas untuk produk saya" tidak mendapat skill tanpa `keywords`, dan terpilih `founder-pricing` (skor 3.76) setelah `keywords` ditambahkan.

## Pembersihan Isi Skill

Sebelum disisipkan, isi `SKILL.md` dibersihkan oleh `pkg/skillclean`, karena model lain di belakang 9Router tidak punya sandbox maupun tool Claude. Yang dibuang:
- frontmatter YAML dan komentar HTML;
- baris yang menyebut path `/mnt/skills`, variabel `${CLAUDE_...}`, tool bawaan Claude (`AskUserQuestion`, `WebSearch`, `subagent_type`, `Agent tool`, `mcp__...`, dan sejenisnya);
- blok kode yang menjalankan program (`python3`, `node`, `bash`, `npm`, dan sejenisnya), karena model tidak bisa menjalankannya;
- judul yang jadi kosong setelah pembersihan.

Menambah aturan cukup menambah satu baris di `lineRules` atau `scriptRules` pada `internal/pkg/skillclean/clean.go`. Pembersihan hanya menghapus; kalimat yang merujuk ke bagian yang dibuang (mis. "jalankan skrip di bawah") tetap ada, jadi skill yang intinya memang bergantung pada script tetap tidak berfungsi penuh. Pada 11 skill `founder-*`, pembersihan menghemat sekitar 3% token.

Untuk melihat hasil akhirnya tanpa memanggil model: `GET /api/skills/preview/<nama>?model=<nama-model>`.

## Budget Token dan SHORT.md

Setiap model punya batas token skill, diatur di `.env`:

```env
SKILL_BUDGET_DEFAULT=3000
SKILL_BUDGETS=*mini*=800,*haiku*=800,*flash*=800
```

`SKILL_BUDGETS` berisi `pola=token`; pola tidak membedakan huruf besar/kecil, `*` adalah wildcard, aturan pertama yang cocok dipakai, dan model yang tidak cocok memakai `SKILL_BUDGET_DEFAULT` (0 = tanpa batas). Bila isi skill (setelah dibersihkan) melebihi budget:
1. diganti `SHORT.md` di folder skill yang sama (ringkasan 200-400 token), bila ada;
2. bila `SHORT.md` tidak ada atau masih melebihi budget, dipotong di batas paragraf dan diakhiri `[... dipotong karena batas token]`.

Varian yang dipakai (`full`, `short`, `truncated`) dicatat di `t_usage_logs`. Token dihitung dengan perkiraan karakter / 4, jadi batasnya perkiraan, bukan angka pasti dari tokenizer model.

## Cache (Redis)

Dua cache di Redis, dengan key berawalan `skillmw:`. Isinya hanya nama folder skill dan skor; key berupa hash, tidak memuat isi pesan.

| Cache | Isi | Masa berlaku |
|---|---|---|
| **Sesi** | skill aktif per percakapan | `SKILL_SESSION_TTL` (45 menit), diperpanjang tiap dipakai |
| **Hasil seleksi** | pesan (dinormalisasi) -> skill terpilih, termasuk "tidak ada yang cocok" | `SKILL_SELECTION_TTL` (1 jam), dan otomatis tidak berlaku saat index dibangun ulang |

**Cache sesi.** Isi skill harus dikirim ulang di setiap request karena model tidak menyimpan apa pun. Dengan cache sesi, pesan lanjutan seperti "oke lanjut" atau "terima kasih" tetap memakai skill yang sama. Skill aktif baru diganti bila skill lain **jauh lebih cocok**: skornya minimal `SKILL_SWITCH_RATIO` (1.5) kali skor skill aktif untuk pesan yang sama.

**Mengenali percakapan.** Format OpenAI tidak punya ID percakapan, jadi dipakai header `X-Conversation-Id` bila client mengirimnya, atau pesan user pertama (yang selalu ikut terkirim). `user_id` (header `X-User-Id`) ikut di-hash supaya percakapan dua user berbeda tidak bertabrakan. Dua percakapan dari pengguna yang sama yang dimulai dengan pesan identik dianggap satu percakapan, kecuali client mengirim `X-Conversation-Id`.

**Cache hasil seleksi.** Pesan yang sama setelah normalisasi (huruf kecil, tanpa kata umum, urutan kata diabaikan) memakai hasil sebelumnya. Dengan BM25 yang hanya butuh mikrodetik, manfaatnya saat ini kecil; cache ini disiapkan untuk Tahap 3 saat pemilihan memakai embedding yang jauh lebih mahal.

**Redis wajib.** `REDIS_ADDR` harus diisi dan Redis harus terjangkau saat start; bila tidak, server berhenti (seperti PostgreSQL). Setelah server berjalan, Redis yang mati di tengah jalan **tidak** menghentikan request: cache dilewati (status cache tercatat `off`) dan bekerja lagi sendiri setelah Redis pulih. Operasi Redis memakai timeout pendek (200 ms) supaya Redis yang lambat tidak memperlambat request.

## Pemilih Cadangan

Seleksi utama tetap tanpa LLM. Model 9Router milik user (sama dengan `model` chat-nya) hanya dipanggil saat skor **ambigu** (`SKILL_FALLBACK_ENABLED=true`):
- dua skill atau lebih bersaing ketat (skor BM25 >= `SKILL_AMBIGUOUS_RATIO` x skor teratas, bawaan 0.8), atau
- skor teratas sedikit di bawah threshold (antara 50% dan 100% dari `SKILL_MIN_SCORE`).

Model diberi pesan user dan maksimal 3 kandidat, lalu hanya boleh menjawab satu nama kandidat atau `none`. Jawaban lain dianggap gagal, dan seleksi kembali ke keputusan berdasarkan skor. Keputusannya ikut disimpan di cache hasil seleksi, jadi pesan yang sama tidak memanggil model lagi. Hasil yang gagal (timeout, jawaban tak dikenali) tidak di-cache.

Hasil uji pada 9Router asli dengan `AI-Crypro`: tiga pesan uji dipilih benar (harga -> `founder-pricing`, pesaing -> `founder-competitors`, sapaan -> `none`), sekitar **2,3-2,9 detik dan sekitar 2.200 token per panggilan**. Token sebesar itu sebagian besar berasal dari prompt tambahan di sisi 9Router/provider, bukan dari prompt middleware (sekitar 200 token); Gemini Flash menunjukkan angka serupa. Artinya setiap pesan ambigu menambah beberapa detik ke request user. `SKILL_FALLBACK_TIMEOUT` (bawaan 5 detik) membatasi tunggu maksimalnya. Model `kr/*` di 9Router saat ini menolak request ("This version of Kiro is no longer supported"), jadi jangan dipakai.

Untuk melihat apakah sebuah pesan akan memicu pemilih cadangan: `GET /api/skills/match?q=...` (field `ambiguous`), atau tambah `&fallback=true` untuk benar-benar menjalankannya.

## Embedding

Embedding memilih skill berdasarkan **makna**, jadi pesan Indonesia bisa cocok dengan deskripsi skill berbahasa Inggris tanpa `keywords`. Embedding **dimatikan selama `EMBEDDING_MODEL` kosong** (seleksi memakai BM25). Saat ini 9Router belum punya provider embedding (`/v1/embeddings` menjawab "No credentials for provider: openai"), jadi bagian ini siap tetapi belum aktif.

Cara mengaktifkan, salah satu:
- tambahkan provider embedding (mis. OpenAI) di dashboard 9Router, lalu isi `EMBEDDING_MODEL` dengan nama modelnya, `EMBEDDING_BASE_URL` dengan alamat 9Router itu, dan `EMBEDDING_API_KEY`;
- atau pasang Ollama dengan model `bge-m3` (mendukung bahasa Indonesia), lalu isi `EMBEDDING_BASE_URL=http://localhost:11434/v1` dan `EMBEDDING_MODEL=bge-m3`.

Perilaku:
- Bila `EMBEDDING_MODEL` diisi, layanan embedding **wajib terjangkau saat start**; server berhenti bila tidak.
- Embedding tiap skill (nama + deskripsi + keywords) dihitung sekali lalu disimpan di `skills/<nama>/.embedding.json` beserta hash teks dan nama modelnya. Dihitung ulang hanya bila deskripsi skill atau modelnya berubah. File ini diabaikan watcher dan tidak ikut dihitung di daftar file skill.
- Saat request, hanya pesan user yang di-embed (batas `EMBEDDING_TIMEOUT`, bawaan 2 detik). Bila gagal, seleksi request itu kembali ke BM25.
- Embedding hanya dipakai bila **semua** skill sudah punya vektor, supaya skor antar skill sebanding.
- Skala skornya kemiripan kosinus (-1..1), jadi punya threshold sendiri: `SKILL_MIN_SIMILARITY`, `SKILL_SWITCH_MARGIN`, `SKILL_AMBIGUOUS_MARGIN`. Nilai bawaan adalah tebakan awal dan perlu disetel lewat `/api/skills/match` setelah model embedding dipilih.

## Eval Skill

Eval mengukur apakah skill benar-benar memperbaiki jawaban, dan apakah `SHORT.md` cukup setara untuk dipakai (lebih hemat token).

**1. Tulis pertanyaan uji** di `skills/<nama>/EVAL.yaml` (ikut terupload dalam zip skill, disarankan 10-20 pertanyaan, maksimal 30):

```yaml
cases:
  - question: Berapa harga yang pas untuk kopi susu 250ml di dekat kampus?
    criteria: Menyebut rentang harga terlalu murah sampai terlalu mahal, membandingkan dengan pesaing, dan memberi satu harga rekomendasi.
  - question: Apakah saya perlu paket bundling?
    criteria: Menimbang margin per paket dan memberi contoh harga paket.
```

`criteria` wajib: itulah yang dibaca hakim untuk menilai.

**2. Jalankan eval:**

```bash
curl -X POST http://localhost:4000/api/evals/runs -H "Content-Type: application/json" \
  -H "X-API-Key: <API_KEYS>" -H "X-User-Id: admin" -H "X-Router-URL: https://<domain-9router>/v1" -H "X-Router-Key: <KEY 9ROUTER>" \
  -d '{"skill":"founder-pricing","model":"AI-Crypro"}'
```

Tanpa `variants`, yang dibandingkan: `full` (SKILL.md), `short` (SHORT.md, bila ada), dan `none` (tanpa skill, sebagai pembanding). `model` (model 9Router yang diuji) wajib, dan key 9Router dikirim di header `X-Router-Key` (401 bila tidak ada). Opsional: `judge_model` (bawaan sama dengan `model`) dan `limit` (jumlah pertanyaan). Eval berjalan di latar belakang dan langsung mengembalikan `run_id`.

**3. Lihat hasil:** `GET /api/evals/runs/<run_id>` berisi skor rata-rata per varian, rata-rata token skill dan token prompt, jumlah yang gagal, serta kesimpulan otomatis, misalnya "SHORT.md setara dengan SKILL.md ... layak dipakai" atau "Skill hampir tidak menaikkan skor dibanding tanpa skill". Tambah `?detail=true` untuk melihat setiap jawaban, skor, dan alasan hakim.

Catatan:
- Hakim (`judge_model`, bawaan sama dengan `model`) menilai 0-10 berdasarkan `criteria`, **tanpa diberi tahu varian mana** yang menjawab, supaya tidak berat sebelah.
- Setiap baris menyimpan `skill_version` (hash SKILL.md + SHORT.md), jadi hasil sebelum dan sesudah skill diedit bisa dibandingkan.
- Biaya: jumlah pertanyaan x jumlah varian x 2 panggilan model (menjawab + menilai). Dengan 20 pertanyaan dan 3 varian = 120 panggilan, dan pada 9Router asli sekitar 2.200 token per panggilan. Jalankan saat skill berubah, bukan terus-menerus.
- Hasil tersimpan di tabel `t_eval_runs`. Eval yang sedang berjalan saat server di-restart berhenti di tengah; hasil yang sudah selesai tetap tersimpan.

## Memori (Context Pack dan Catatan)

Tahap 4 menambahkan dua jenis memori. Keduanya **per user** (kolom `user_id`) dan disimpan di PostgreSQL: context pack dan catatan milik user A tidak pernah disisipkan ke chat user B. Di endpoint `/api/*`, admin bisa melihat dan mengelola milik semua user, tetapi chat admin tetap hanya memakai miliknya sendiri.

**1. Context pack (`m_context_packs`)**: ringkasan tetap yang selalu relevan, misalnya gambaran bisnis, istilah, atau aturan yang berlaku. Semua context pack yang aktif disisipkan di setiap request setelah skill, diurutkan menurut nama, dan dibatasi `CONTEXT_PACK_MAX_TOKENS` (dipotong di batas paragraf). Teksnya di-cache di memori selama 30 detik, dan perubahan lewat API langsung mengosongkan cache. Bila database gagal dibaca, teks terakhir tetap dipakai.

```bash
curl -X POST http://localhost:4000/api/context-packs -H "Content-Type: application/json" \
  -H "X-API-Key: <API_KEYS>" -H "X-User-Id: user-123" \
  -d '{"name":"bisnis","content":"Kedai kopi di Bandung, jual kopi susu literan via Instagram dan GoFood. Target: mahasiswa."}'
```

**2. Catatan memori (`t_memory_notes`)**: pengalaman yang pernah dicoba beserta hasilnya, dengan bentuk *topik, yang dicoba, hasil, sumber hasil (`user`/`data`), pelajaran*. Siklusnya:

1. **Ekstraksi otomatis.** Setelah respons chat selesai, pesan user terakhir diperiksa. Bila memuat sinyal hasil (berhasil, gagal, omzet, laku, naik, turun, untung, rugi, terjual, ...), model chat milik user (dengan key-nya) mengekstraknya di latar belakang menjadi catatan berstatus `proposed`. Model hanya boleh mencatat hal yang sudah dicoba dan hasilnya sudah terjadi, bukan rencana atau saran asisten. Pesan tanpa sinyal tidak memanggil model sama sekali. Pesan yang sama (misalnya request diulang client) tidak diekstrak dua kali.
2. **Konfirmasi lewat API.** Catatan usulan tidak dipakai sampai dikonfirmasi (`POST /:id/confirm` -> `active`), dikoreksi (`PUT /:id` -> `corrected`, langsung dipakai), ditolak (`POST /:id/reject`), atau dihapus. Catatan yang ditolak atau dihapus tetap disimpan barisnya supaya isi yang sama tidak diusulkan lagi. Catatan manual (`POST /api/memory-notes`) langsung aktif.
3. **Penyisipan.** Untuk setiap pesan, catatan `active`/`corrected` yang belum kedaluwarsa dicari dengan pencarian teks PostgreSQL (kolom `search_vector` dan index GIN, konfigurasi `simple`). Yang dipakai hanya catatan yang memuat paling sedikit `MEMORY_MIN_MATCH` kata pesan, maksimal `MEMORY_MAX_NOTES`, dan masing-masing dibatasi `MEMORY_NOTE_MAX_TOKENS`. Model diminta memperlakukannya sebagai **pengingat yang perlu dicek ulang, bukan larangan**: bila rencana user mengulang pola yang dulu gagal, model menyebutkannya lebih dulu lalu menawarkan alternatif.
4. **Kedaluwarsa.** Catatan berlaku `MEMORY_NOTE_TTL` sejak dikonfirmasi/dikoreksi (bawaan 90 hari), karena hasil bisnis bisa basi.

```bash
# catatan usulan yang menunggu konfirmasi
curl "http://localhost:4000/api/memory-notes?status=proposed" -H "X-API-Key: <API_KEYS>" -H "X-User-Id: user-123"
# catatan apa yang akan ikut untuk sebuah pesan
curl "http://localhost:4000/api/memory-notes/match?q=mau+promo+diskon+50%25+lagi" -H "X-API-Key: <API_KEYS>" -H "X-User-Id: user-123"
```

Contoh hasil uji nyata: setelah ada catatan "promo diskon 50% kopi susu: penjualan naik 2x tapi rugi Rp1,2 juta", pertanyaan "aku mau bikin promo diskon 50% lagi" dijawab model dengan menyebut hasil itu lebih dulu, lalu menyarankan bundling atau diskon 15-20%.

Catatan:
- PostgreSQL tidak punya kamus bahasa Indonesia, jadi akhiran kata ditangani dengan pencarian prefix (`diskon:*` juga cocok dengan "diskonnya"). Hasilnya lalu disaring lagi dengan tokenizer yang sama dengan seleksi skill.
- Pencarian catatan dibatasi `MEMORY_LOOKUP_TIMEOUT`. Bila lewat atau database gagal, request jalan tanpa catatan (gagal terbuka).
- Ekstraksi yang berjalan bersamaan dibatasi `MEMORY_EXTRACT_CONCURRENCY`. Bila antrean penuh, ekstraksi dilewati. Biaya token ekstraksi dicatat di log aplikasi (`ekstraksi memori selesai`).
- `MEMORY_EXTRACT_ENABLED=false` = ekstraksi mati, tetapi catatan manual dan penyisipan tetap berjalan. `MEMORY_MAX_NOTES=0` mematikan penyisipan catatan.

## MCP (Canva, Figma, dan lainnya)

MCP (Model Context Protocol) memberi model akses ke aplikasi lain, misalnya mencari atau membuat desain di Canva. 9Router tidak bisa terhubung ke server MCP, jadi **middleware yang menjadi klien MCP**. Model di 9Router mendukung tool calling (sudah diuji dengan `AI-Crypro`): model meminta "panggil tool X", lalu middleware yang menjalankannya.

```
Client ──chat──▶ middleware ──(chat + tool yang cocok)──▶ 9Router ──▶ model
                    ▲  │                                              │
                    │  └──▶ server MCP (Canva, ...) ◀── "panggil tool" ┘
                    │            │
                    └── jawaban akhir ◀── model ◀── 9Router ◀── hasil tool
```

**1. Daftarkan server** di `mcp.json`. File ini tanpa rahasia dan boleh di-commit; perubahan berlaku setelah restart.

```json
{
  "servers": {
    "canva": { "url": "https://mcp.canva.com/mcp", "keywords": ["canva", "desain", "poster"] },
    "tokoku": { "url": "https://mcp.tokoku.example/mcp", "headers": { "Authorization": "Bearer ${TOKOKU_TOKEN}" } }
  }
}
```

- `keywords`: kata yang ikut dicocokkan dengan pesan user saat memilih tool.
- `headers`: untuk server ber-API key. `${NAMA_ENV}` diambil dari `.env`, supaya rahasia tidak tertulis di `mcp.json`.
- `"enabled": false` mematikan sebuah server.
- Yang didaftarkan di sini hanya server MCP HTTP (Streamable HTTP). Server `npx`/stdio dari GitHub dipasang lewat [Pasang MCP dari GitHub](#pasang-mcp-dari-github-lewat-chat), yang menjalankannya di container dan membungkusnya menjadi HTTP.

**2. Login sekali per server (OAuth)**: buka `http://<BASE_URL>/api/mcp/servers/canva/connect` di browser, lalu login dan setujui akses. Middleware otomatis mencari alamat login, mendaftarkan dirinya sebagai aplikasi OAuth, dan memakai PKCE. Setiap user login ke **akunnya sendiri** (kecuali server admin dengan login bersama, lihat di bawah). Token disimpan terenkripsi (`MCP_TOKEN_KEY`) di `m_mcp_credentials` dan diperpanjang otomatis. `BASE_URL` harus bisa dibuka dari browser, karena halaman login kembali ke `BASE_URL/api/mcp/oauth/callback`.

**Tiga jenis server MCP:**

| Jenis | Dibuat | Berlaku untuk | Login |
|---|---|---|---|
| Bawaan | `mcp.json` (butuh restart) | semua user | tiap user login sendiri |
| Dari admin | admin lewat chat / `POST /api/mcp/servers` (tanpa restart) | **semua user** | dipilih admin: `shared_login: true` = semua memakai login admin (cocok untuk GitHub/database organisasi); `false` = tiap user login sendiri (Canva/Figma pribadi) |
| Pribadi | user biasa lewat chat / API | hanya user itu | user itu sendiri |

Token login tersimpan per (user, server) di `m_mcp_credentials`. Link login dari chat ditandatangani supaya tidak bisa dipakai untuk user lain. Login bersama hanya bisa diurus admin pembuatnya; user lain tidak diberi link login. Server admin hanya bisa dihapus admin.

**Kelola MCP lewat chat:** "mcp apa aja yang tersambung?", "konekan saya ke canva" (diberi link login), "tambahkan MCP notion, url-nya https://..." (URL dicek, langsung aktif: pribadi bila diminta user biasa, untuk semua user bila diminta admin), dan "hapus MCP notion". Tool bawaan: `list_connections`, `connect_service`, `add_mcp_server`, `remove_mcp_server`. Detail di [`docs/10-mcp.md`](docs/10-mcp.md).

**3. Setelah itu berjalan sendiri di setiap chat:**
- Tool dipilih **otomatis sesuai pesan** (BM25 pada nama server, `keywords`, nama dan deskripsi tool, dari dua pesan user terakhir). Maksimal `MCP_MAX_TOOLS` tool dengan skor ≥ `MCP_TOOL_MIN_SCORE`.
- Pesan yang tidak menyinggung tool apa pun ("halo") diteruskan biasa tanpa tool dan tanpa tambahan token.
- Bila model meminta tool, middleware menjalankannya (bersamaan, batas `MCP_TOOL_TIMEOUT`), memberikan hasilnya ke model, dan mengulanginya sampai ada jawaban akhir (maks `MCP_MAX_STEPS` putaran).
- **Semua tool diizinkan**, termasuk yang mengubah data. Pakai akun yang aman untuk diubah.
- Bila client sudah membawa `tools` sendiri, tool MCP tidak ditambahkan.

```bash
# status + link login
curl http://localhost:4000/api/mcp/servers -H "X-API-Key: <API_KEYS>" -H "X-User-Id: user-123"
# tool yang akan terpilih untuk sebuah pesan
curl "http://localhost:4000/api/mcp/tools?q=buatkan+poster+promo+di+canva" -H "X-API-Key: <API_KEYS>" -H "X-User-Id: user-123"
```

Catatan:
- Chat yang memakai tool kira-kira **2x lebih lambat dan mahal**, karena model dipanggil minimal dua kali. Hasil uji: sekitar 7,5 detik dan sekitar 5.500 token prompt. Usage di respons adalah jumlah semua putaran.
- Untuk client streaming, jawaban dikirim **sekaligus di akhir** (bukan per kata). Selama tool berjalan, client menerima komentar SSE `: keepalive` supaya koneksi tidak putus.
- Tool yang gagal tidak menggagalkan chat: model menerima `ERROR: ...` dan menjelaskannya. Hasil berupa gambar hanya disebutkan, tidak bisa dilihat model.
- **Figma:** server MCP resminya menolak pendaftaran klien (`403`, hanya aplikasi yang disetujui Figma), jadi `figma` di `mcp.json` diset `enabled: false`. Alternatifnya, jalankan server MCP Figma pihak ketiga yang memakai personal access token sebagai layanan HTTP terpisah.
- Respons yang membawa tool diberi header `X-MCP-Tools`. Pemakaian tool dicatat di `t_usage_logs` (`mcp_tools_offered`, `mcp_tool_calls`, `mcp_tool_names`, `mcp_steps`) dan diringkas di bagian `mcp` pada `/api/usage-logs/summary`.

Penjelasan lengkap: [`docs/10-mcp.md`](docs/10-mcp.md).

## Pasang MCP dari GitHub lewat Chat

Chat: *"tolong pasangkan https://github.com/bytebase/dbhub jadi MCP"*. Alurnya:

1. **Analisis.** Repo diunduh ke `open_source_mcp/<owner>__<repo>`, dikunci ke satu commit, dan diperiksa (package.json, pyproject, `.env.example`, README). **Tidak ada kode yang dijalankan.**
2. **Konfirmasi.** AI menjelaskan rencana (nama, perintah install & start, nama variabel rahasia seperti `DSN`), lalu user mengonfirmasi.
3. **Persetujuan.** Pengajuan user biasa disimpan di `m_mcp_installs` dengan status `pending_review` (tidak hilang walau admin lama tidak membukanya) sampai admin menyetujui/menolak (*"setujui pemasangan dbhub"* atau API). Pengajuan admin langsung disetujui dan berlaku untuk semua user, dengan pilihan **wajib**:
   - **sharing**: satu container berisi data rahasia admin dipakai semua user;
   - **tidak sharing**: jadi template; tiap user mengisi data rahasianya sendiri dan mendapat container sendiri (`m_mcp_install_instances`), tanpa persetujuan lagi.
4. **Data rahasia** (password, DSN, token) diisi lewat **link form** bertanda tangan (24 jam), bukan di chat, lalu disimpan terenkripsi (`MCP_TOKEN_KEY`).
5. **Jalan.** Container `mcpx-<nama>` dari image `docker/mcp-runner` meng-clone commit yang disetujui, menjalankan perintah install, lalu menjalankan server (stdio dibungkus supergateway menjadi HTTP). Begitu siap, server langsung terdaftar dan tool-nya bisa dipakai di chat.

Isolasi container: bukan root, semua capability dibuang, `no-new-privileges`, tanpa folder VPS, dibatasi memori/CPU/proses (`MCP_RUNNER_*`), hanya di network `middleware_mcp_net`, dan env middleware (password DB, `JWT_SECRET`, ...) tidak pernah ikut. Middleware memanggil Docker lewat `docker-proxy`, bukan socket Docker langsung. Bila container gagal, ia dihentikan dan log terakhirnya dicatat; middleware tetap jalan.

Tool chat: `install_mcp_from_github` (`analyze`, `submit`) dan `manage_mcp_install` (`list`, `env_link`, `start`, `stop`, `remove`, admin: `approve`, `reject`). Fitur mati bila `MCP_DOCKER_HOST` kosong (di docker-compose otomatis aktif). Penjelasan lengkap, batas keamanan, dan cara menyalakan di laptop: [`docs/13-pasang-mcp-dari-github.md`](docs/13-pasang-mcp-dari-github.md).

## Pencarian Web lewat Chat

Tool bawaan **`web_search`** mencari di internet lewat **SearXNG** yang dijalankan sendiri (service `searxng` di docker-compose, tanpa API key). Tool hanya ditawarkan bila pesan butuh info dari web (cari, terbaru, berita, tren, pesaing, kompetitor, referensi, riset, link, ...). Model menulis query singkat, boleh beberapa kali, lalu menjawab dengan sumbernya.

- `WEB_SEARCH_URL` (di Docker otomatis `http://searxng:8080`; kosong = mati), `WEB_SEARCH_MAX_RESULTS` (bawaan 8), `WEB_SEARCH_TIMEOUT` (bawaan 15 detik).
- Cek manual: `GET /api/web-search?q=harga+kopi+susu+bandung`.

## Buat Skill lewat Chat

Chat: *"buatkan skill meta-ads untuk kedai kopi"*. Tool bawaan **`create_skill`** (ditawarkan bila pesan berisi kata "skill" dan kata kerja seperti buat/bikin/tulis/cari) membuat model menulis isi `SKILL.md` (dan `SHORT.md` opsional). Middleware menyusun frontmatter yang benar (`name`, `description`, `keywords`), memasangnya lewat proses upload yang sama, lalu chat membalas dengan link **Download zip**. Skill baru langsung bisa terpilih di chat berikutnya. Skill dengan nama sama tidak ditimpa tanpa persetujuan user. Bisa digabung dengan `web_search` untuk riset dulu sebelum menulis skill.

## Gambar lewat Chat

Chat seperti "buatkan gambar logo kedai kopi, minimalis warna coklat" bisa menghasilkan gambar yang langsung tampil di jawaban (`![alt](BASE_URL/images/<nama>.png)`). Middleware memberi model tool bawaan **`generate_image`** (lewat putaran tool yang sama dengan MCP):

1. Tool hanya ditawarkan bila pesan menyinggung gambar (gambar, ilustrasi, logo, foto, poster, ikon, ...), jadi chat biasa tidak bertambah token.
2. Model menulis prompt detail lalu memanggil tool; middleware memanggil `POST /images/generations` di 9Router dengan key user, memakai model gambar yang tersedia di akun user itu.
3. Gambar disimpan di `IMAGE_PATH` dan disajikan di `GET /images/<nama>`; model menampilkannya dengan markdown.
4. URL, prompt, alt, model, dan ukurannya dicatat di tabel `t_generated_images`, bisa dibaca lewat `GET /api/images`.

Model gambar **tidak ditulis di `.env`**: daftarnya diambil otomatis dari akun 9Router user (`GET /v1/models/image`, di-cache 10 menit per akun) lalu dicoba berurutan; model yang menolak dilewati sementara. Akun tanpa model gambar tidak ditawari tool ini. Saat ini `cx/gpt-image-*` butuh ChatGPT Plus/Pro pada akun yang terhubung ke 9Router, dan `ag/gemini-3.1-flash-image` (gratis) sering penuh. `BASE_URL` harus bisa dibuka aplikasi chat supaya gambar tampil. Penjelasan lengkap: [`docs/11-gambar.md`](docs/11-gambar.md).

## Akun, Login, dan Role

Untuk user yang tidak punya aplikasi pihak ketiga (mis. lewat web middleware AI). Tabel `m_users` berisi `id`, `email`, `password` (bcrypt), dan `role` (`user`/`admin`).

| Endpoint | Hasil |
|---|---|
| `POST /api/auth/register` `{"email","password"}` | akun **selalu role `user`** + token (field `role` diabaikan). 409 bila email terdaftar |
| `POST /api/auth/login` | token (401 `Email atau password salah`, pesan sama untuk email tidak ada) |
| `POST /api/auth/refresh` `{"refresh_token"}` | pasangan token baru (role dibaca ulang dari database) |
| `GET /api/auth/me` | akun yang sedang login |

- Access token 15 menit (`JWT_ACCESS_TTL`), refresh token 30 hari (`JWT_REFRESH_TTL`), JWT HS256 dengan `JWT_SECRET`. Refresh token tidak bisa dipakai sebagai access token.
- Refresh token tidak disimpan di database, jadi tidak bisa dicabut satu per satu. Mengganti `JWT_SECRET` mencabut semua token.
- Register **selalu** menghasilkan role `user`, walaupun body berisi `"role":"admin"`. Register terbuka tanpa key/token, jadi role admin sengaja tidak bisa dipilih di sini supaya orang lain tidak bisa mendaftar sendiri sebagai admin.
- **Admin dibuat langsung di database** (lihat contoh di bawah), atau dari `ADMIN_EMAIL`/`ADMIN_PASSWORD` di `.env` saat start.
- SDK OpenAI bisa langsung dipakai: isi `api_key` dengan access token. `Authorization` berisi bukan-JWT (mis. `sk-...`) diabaikan dan pemeriksaan pindah ke `X-API-Key`.

**Contoh: register user biasa**

```bash
curl -X POST http://localhost:4000/api/auth/register -H "Content-Type: application/json" \
  -d '{"email":"budi@contoh.com","password":"rahasia123"}'
```

**Contoh: membuat admin (lewat database)**

```bash
# 1. register seperti biasa (hasilnya role user)
curl -X POST http://localhost:4000/api/auth/register -H "Content-Type: application/json" \
  -d '{"email":"pemilik@contoh.com","password":"password-admin-yang-kuat"}'
```

```sql
-- 2. naikkan jadi admin langsung di PostgreSQL (psql, DBeaver, pgAdmin, ...)
UPDATE m_users SET role = 'admin' WHERE email = 'pemilik@contoh.com';

-- cek
SELECT id, email, role FROM m_users WHERE email = 'pemilik@contoh.com';

-- menurunkan lagi jadi user
UPDATE m_users SET role = 'user' WHERE email = 'pemilik@contoh.com';
```

```bash
# 3. login ulang supaya token membawa role admin
curl -X POST http://localhost:4000/api/auth/login -H "Content-Type: application/json" \
  -d '{"email":"pemilik@contoh.com","password":"password-admin-yang-kuat"}'
#    -> data.user.role = "admin", data.access_token = "eyJ..."

# 4. pastikan role-nya admin
curl http://localhost:4000/api/auth/me -H "Authorization: Bearer <access_token>"

# contoh pakai token admin: lihat pengajuan MCP dari user yang menunggu persetujuan
curl "http://localhost:4000/api/mcp/installs?status=pending_review" -H "Authorization: Bearer <access_token>"
```

Role tersimpan di dalam token, jadi perubahan role lewat database baru berlaku setelah **login ulang** atau **refresh token** (access token lama tetap membawa role lama sampai kedaluwarsa, paling lama 15 menit). Email di database tersimpan huruf kecil.

Cara lain: isi `ADMIN_EMAIL`/`ADMIN_PASSWORD` di `.env` lalu restart; akun admin dibuat otomatis saat start (log `akun admin dibuat`).

Detail: [`docs/12-akun-dan-login.md`](docs/12-akun-dan-login.md).

## Index Skill

Saat start, middleware membaca `skills/*/SKILL.md` dan menyimpan `name`, `description`, `keywords`, serta ada tidaknya `SHORT.md` di memori. Isi skill tidak disimpan di index; dibaca dari disk saat skill terpilih.

- Index dibangun ulang otomatis setelah upload sukses, dan oleh **watcher** yang memeriksa folder `skills/` tiap `SKILL_WATCH_INTERVAL` (5 detik; 0 = dimatikan). File yang ditambah, diubah, atau dihapus langsung di disk terbaca otomatis. Perubahan baru dimuat setelah stabil dua kali pemeriksaan berturut-turut, supaya file yang sedang ditulis tidak terbaca setengah.
- Index baru dibangun terpisah lalu ditukar dalam satu langkah atomik, jadi request yang sedang berjalan tidak pernah melihat index setengah jadi.
- Folder tanpa `SKILL.md`, dengan frontmatter rusak, atau `description` kosong dilewati dan dicatat di log, tanpa menggagalkan skill lain.
- Folder berawalan `.` (staging dan backup upload) diabaikan.

## Impor Skill dari GitHub lewat Chat

Tulis di chat: *"ambil skill dari https://github.com/owner/repo lalu jadikan zip"*. Tool bawaan **`import_skills_from_github`** (hanya ditawarkan bila pesan berisi link `github.com`) mengunduh repo, menyusun setiap folder ber-`SKILL.md` (di kedalaman berapa pun) menjadi zip skill yang rapi, memasangnya lewat proses upload yang sama (hanya file teks), lalu chat membalas dengan daftar skill dan link **Download zip** (`/api/skills/export?names=...`). Skill yang sudah ada tidak ditimpa tanpa persetujuan user. Semua user boleh memasang skill, dan skill berlaku untuk semua user. Detail: [`docs/03-skill.md`](docs/03-skill.md).

## Daftar Skill

```bash
curl http://localhost:4000/api/skills -H "X-API-Key: <API_KEYS>"
```

Mengembalikan array di `data`, terurut menurut nama: `name`, `description` (dari frontmatter), `files`, `has_short`, `size_bytes`, dan `updated_at` (waktu ubah file terbaru). Folder berawalan `.` (staging/backup upload) tidak ikut. Skill yang `SKILL.md`-nya hilang atau frontmatter-nya rusak tetap muncul dengan field `error`.

## Upload Skill (zip)

```bash
curl -X POST http://localhost:4000/api/skills/upload -H "X-API-Key: <API_KEYS>" \
  -F "file=@copywriting.zip" \
  -F "overwrite=false"
```

- Form field: `file` (wajib, `.zip` atau `.skill`, maks 10 MB) dan `overwrite` (opsional, `true` untuk mengganti skill yang sudah ada).
- Isi zip, salah satu bentuk:
  - `SKILL.md` di root zip (satu skill), atau
  - `<folder>/SKILL.md` untuk tiap skill (satu atau banyak skill dalam satu zip).
- `SKILL.md` wajib berfrontmatter `name` (huruf kecil/angka, dipisah `-` atau `_`, maks 64) dan `description` (maks 1024, jelaskan kapan skill dipakai). Nama folder hasil pasang = `name`.
- Hanya file teks yang dipasang (`.md .txt .json .yaml .yml .csv`). Script dan file lain dibuang dan dilaporkan di `skipped_files`.
- Pemasangan atomik: diekstrak ke folder staging (`.upload-*`) dulu, baru di-rename. Skill yang diganti dibackup sementara (`.old-*`) dan dipulihkan bila ada yang gagal. Loader skill harus mengabaikan folder berawalan `.`.
- Dilindungi dari zip slip, symlink, dan zip bomb (maks 500 file, 2 MB per file, 20 MB total setelah diekstrak).
- Butuh token login atau `X-API-Key` seperti endpoint lain. Skill yang dipasang berlaku untuk semua user.

## Amplop Respons

Bentuk seragam untuk respons API (dipakai `internal/pkg/response`):

```json
{ "status": "success", "message": "Berhasil", "data": { } }
```

```json
{ "status": "fail", "message": "Validasi gagal", "errors": [ { "field": "email", "message": "wajib diisi" } ] }
```

## Catatan Desain

- **Middleware global**: `Recovery` (panic -> 500 amplop), `HTTPLogger`
  (request log ke zap), `CORS` (mengizinkan `Authorization`, `X-API-Key`, `X-Router-URL`,
  `X-Router-Key`, `X-User-Id`, `X-Conversation-Id`), dan `Auth` (token login atau `X-API-Key`).
- **Data per user**: `userctx.Scope` + `database.ByUser`: admin = semua data, user = miliknya.
  Sisipan chat (context pack, catatan) selalu milik user yang chat sendiri.
- **9Router milik user**: alamat, key, dan model dibawa di context request sampai ke semua
  panggilan turunan (tool, gambar, pemilih cadangan, ekstraksi memori di latar belakang, eval).
  Alamat ke jaringan internal ditolak (`ROUTER_ALLOW_PRIVATE_NETWORK=false`).
- **Error handling terpusat**: `apperror.Handle` memetakan `AppError`,
  `gorm.ErrRecordNotFound`, dan unique-violation PostgreSQL ke amplop yang sesuai.
- **Logging** zap menulis `logs/combined.log` (info+) & `logs/error.log` (error+),
  plus console berwarna saat bukan production.
- **Dependency injection** & wiring terpusat di `internal/router`.

## Status tahap (dokumen *Desain Middleware Skill untuk 9Router*)

| Tahap | Isi | Status |
|---|---|---|
| 1 | Endpoint, proxy streaming, loader skill, BM25, threshold, satu skill per request | selesai |
| 2 | Budget per model, `SHORT.md`, cache (sesi, hasil seleksi, embedding), log pemakaian, pembersihan isi, reload otomatis | selesai |
| 3 | Embedding, pemilih cadangan, pengukuran skill terpakai, eval | selesai; embedding menunggu provider (`EMBEDDING_MODEL`) |
| 4 | Context pack, catatan pelajaran, ekstraksi di latar belakang | selesai |
| + | MCP: middleware menjadi klien MCP karena 9Router tidak bisa (lihat `docs/10-mcp.md`) | selesai; Canva siap login, Figma resmi menolak pendaftaran klien |
| + | 9Router milik user: `X-Router-URL`, `X-Router-Key`, `model` dari client; tidak ada key/model/alamat di `.env` | selesai |
| + | Data per user (`X-User-Id`) dan secret aplikasi `API_KEYS` | selesai |
| + | Akun: register, login, refresh token, role admin/user (admin lihat semua data) | selesai |
| + | MCP dari admin berlaku untuk semua user (`shared_login`), MCP pribadi per user | selesai |
| + | Gambar lewat chat (model gambar dari akun 9Router user) | selesai |
| + | Pencarian web (SearXNG), buat skill lewat chat, impor skill dari GitHub | selesai |
| + | Pasang MCP dari GitHub: persetujuan admin, form rahasia, container Docker terpisah, sharing / tidak sharing | selesai; diuji dengan Docker palsu, belum dengan container sungguhan |
