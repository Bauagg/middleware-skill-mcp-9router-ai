# middleware-skill-ai

Kerangka backend **Go + Gin + GORM (PostgreSQL)** untuk proyek *Skill Middleware 9Router*.
Konvensi arsitektur mengikuti template Express+TypeScript
[`Bauagg/template-express-ts-backend`](https://github.com/Bauagg/template-express-ts-backend),
diportalkan ke gaya idiomatik Go.

> Ini **kerangka inti** (config, logger, koneksi DB, middleware global, amplop
> respons, error handling terpusat). Domain contoh CRUD/auth (users, companies,
> JWT, bcrypt) **sudah dihapus** karena tidak dibutuhkan middleware. Logika inti
> middleware — proxy `/v1/chat/completions` ke 9Router, pemilihan skill (BM25),
> dan injeksi system prompt — dibangun di atas kerangka ini pada tahap berikutnya.

> **Dokumentasi lengkap** (cara kerja, struktur kode, skill, memori, API, konfigurasi, deploy, dan solusi error umum) ada di folder [`docs/`](docs/README.md).

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

## Struktur Folder

```
middleware-skill-ai/
├── cmd/server/main.go            # Entry point (config -> logger -> DB -> middleware -> router -> run)
├── internal/
│   ├── config/config.go          # Muat .env -> struct Config
│   ├── logger/logger.go          # zap: logs/combined.log + logs/error.log + console
│   ├── database/database.go      # Koneksi GORM + AutoMigrate (siap dipakai)
│   ├── middleware/               # cors, http-logger (morgan-like), recovery (panic->500)
│   ├── router/router.go          # Agregasi route (titik daftar endpoint middleware)
│   ├── services/                 # satu folder per domain (controller, service, dto, route, ...)
│   │   ├── skillstore/           # upload, daftar, dan Render skill (bersihkan, budget, SHORT.md); preview
│   │   ├── skillindex/           # index skill di memori, pemilih skill (BM25), skill aktif percakapan, watcher
│   │   ├── skillcache/           # cache Redis: skill aktif per percakapan + hasil seleksi
│   │   ├── proxy/                # proxy /v1/* + penyisipan skill, context pack, catatan, dan putaran tool MCP di chat/completions
│   │   ├── skillfallback/        # pemilih cadangan: model 9Router memilih skill saat skor ambigu
│   │   ├── eval/                 # eval skill: EVAL.yaml -> jawaban model -> skor hakim -> t_eval_runs
│   │   ├── contextpack/          # context pack (m_context_packs): ringkasan tetap yang selalu disisipkan
│   │   ├── memorynote/           # catatan memori (t_memory_notes): ekstraksi, konfirmasi, pencarian teks
│   │   ├── mcp/                  # koneksi MCP (Canva, ...): mcp.json, login OAuth (m_mcp_credentials), pilih & jalankan tool
│   │   └── usagelog/             # log pemakaian di PostgreSQL (t_usage_logs) + GET /api/usage-logs
│   ├── clients/                  # klien layanan pihak ketiga, satu file per layanan
│   │   ├── 9router.go            # koneksi ke 9Router (reverse proxy, timeout, format error, pengamatan token)
│   │   ├── redis.go              # koneksi Redis (timeout pendek, gagal = cache miss)
│   │   ├── embedding.go          # klien embedding umum (POST /embeddings: 9Router, Ollama, dll.)
│   │   ├── mcp.go                # klien server MCP (Streamable HTTP, SDK resmi go-sdk): daftar & panggil tool
│   │   └── mcpauth.go            # discovery OAuth server MCP + pendaftaran klien otomatis (DCR)
│   └── pkg/                      # pustaka bersama, tidak tahu soal domain
│       ├── response/             # Amplop {status,message,data,errors}
│       ├── apperror/             # AppError + Handle + BindingErrors
│       ├── frontmatter/          # parser frontmatter SKILL.md (name, description, keywords)
│       ├── bm25/                 # tokenizer (Indonesia + Inggris) dan skor BM25
│       ├── openai/               # membaca field usage dari respons ala OpenAI (JSON dan SSE)
│       ├── tokens/               # perkiraan jumlah token (karakter / 4) dan pemotongan teks
│       ├── skillclean/           # pembersih isi SKILL.md dari bagian khusus Claude (aturan mudah ditambah)
│       ├── modelbudget/          # budget token per model (pola nama model -> token)
│       ├── uuidparam/            # validasi parameter path :id berformat UUID (400 bila bukan)
│       ├── vector/               # kemiripan kosinus untuk seleksi embedding
│       └── secretbox/            # enkripsi AES-256-GCM untuk token login MCP di database
├── docker/redis-dev/             # Redis DEV (docker compose) untuk dijalankan di VPS, port 6380
├── docs/                         # dokumentasi lengkap (mulai dari docs/README.md)
├── Dockerfile                    # image middleware (build 2 tahap, user non-root, healthcheck)
├── docker-compose.yml            # menjalankan middleware di VPS (lihat docs/09-menjalankan-dan-deploy.md)
├── mcp.json                      # daftar server MCP (lihat docs/10-mcp.md)
├── skills/                       # folder skill (dipakai middleware, tahap lanjutan)
├── logs/                         # output log (dibuat otomatis)
├── .env.example
└── go.mod
```

## Menjalankan

```bash
# 1. Siapkan env
copy .env.example .env      # Linux/macOS: cp .env.example .env
#    isi DB_* (PostgreSQL), REDIS_ADDR (Redis), AI_ROUTER_BASE_URL dan AI_ROUTER_API_KEY (9Router); semuanya wajib

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
berhasil (bila tidak, server berhenti dengan pesan error), lalu tabel `t_usage_logs`
dimigrasikan otomatis.

## Redis DEV (Docker)

Redis utama di VPS (`host.docker.internal:6379`) hanya bisa dipakai dari container di VPS. Untuk pengembangan dari komputer lokal, jalankan Redis DEV terpisah dari folder `docker/redis-dev/`:

```bash
# 1. salin folder ke VPS (dari root proyek)
scp -r docker/redis-dev root@149.129.215.19:/opt/redis-dev

# 2. di VPS
cd /opt/redis-dev
cp .env.example .env          # isi REDIS_DEV_PASSWORD (atau salin .env lokal yang sudah berisi password)
docker compose up -d
docker compose ps             # STATUS harus healthy
```

Lalu di `.env` proyek: `REDIS_ADDR=149.129.215.19:6380`, `REDIS_PASSWORD` sama dengan `REDIS_DEV_PASSWORD`, dan `REDIS_TIMEOUT=2s` (Redis di VPS berjarak 0,3-0,7 detik dari komputer lokal; bawaan 200 ms akan selalu timeout).

Isi `docker/redis-dev/`:
- `docker-compose.yml`: `redis:7.4-alpine`, port 6380, data permanen di volume `redis_dev_data`, restart otomatis, healthcheck. Password diambil dari `.env`, tidak ditulis di file yang di-commit.
- `redis.conf`: batas memori 256 MB (key lama dibuang bila penuh), dan perintah berbahaya (`FLUSHALL`, `FLUSHDB`, `CONFIG`, `DEBUG`, `MODULE`, `REPLICAOF`) dimatikan karena port ini terbuka ke internet.

Bila port 6380 tidak bisa dijangkau setelah container jalan, buka port itu di firewall VPS / security group penyedia cloud (seperti port 5433 untuk PostgreSQL DEV).

## Endpoint (saat ini)

| Method | URL | Keterangan |
|---|---|---|
| GET | `/` | `Server is running` |
| GET | `/health` | `{ status: ok }` |
| GET | `/api/ping` | `{ message: pong }` |
| GET | `/api/skills` | Daftar skill yang terpasang |
| POST | `/api/skills/upload` | Upload skill dalam bentuk zip |
| GET | `/api/skills/match?q=...&limit=5&fallback=false` | Skill apa yang akan terpilih untuk sebuah pesan, skor kandidat, dan apakah skornya ambigu (`fallback=true` ikut menjalankan pemilih cadangan) |
| GET | `/api/skills/preview/:name?model=...&budget=...` | Teks skill persis seperti yang akan disisipkan (sudah dibersihkan dan sesuai budget) |
| POST | `/v1/chat/completions` | Proxy ke 9Router; skill yang relevan, context pack aktif, dan catatan memori yang cocok disisipkan ke system prompt |
| ANY | `/v1/*` | Proxy polos ke 9Router (mis. `/v1/models`) |
| GET | `/api/usage-logs?page=&limit=&model=&skill=` | Daftar log pemakaian, terbaru lebih dulu |
| GET | `/api/usage-logs/summary?from=&to=` | Ringkasan per skill (request, rata-rata token tambahan, token, latensi), hit/miss cache, varian skill, dan penyisipan memori |
| POST | `/api/evals/runs` | Mulai eval skill (berjalan di latar belakang), body `{"skill":"...","variants":["full","short","none"]}` |
| GET | `/api/evals/runs?skill=&limit=` | Daftar eval terbaru |
| GET | `/api/evals/runs/:id?detail=true` | Hasil eval per varian beserta kesimpulannya |
| GET | `/api/mcp/servers` | Status server MCP (Canva, Figma, ...) dan link login |
| GET | `/api/mcp/servers/:name/connect` | Login OAuth server MCP (buka di browser) |
| POST | `/api/mcp/servers/:name/refresh` | Ambil ulang daftar tool server MCP |
| DELETE | `/api/mcp/servers/:name/connection` | Hapus login server MCP |
| GET | `/api/mcp/oauth/callback` | Tujuan balik setelah login OAuth (dipanggil browser) |
| GET | `/api/mcp/tools?q=` | Tool MCP yang terhubung dan yang akan terpilih untuk sebuah pesan |
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

Arahkan client OpenAI-compatible ke middleware ini sebagai pengganti 9Router; selebihnya sama. Alamat 9Router diatur di `AI_ROUTER_BASE_URL` (wajib, mis. `https://host/v1`); path `/v1/xyz` dari client diteruskan ke `<AI_ROUTER_BASE_URL>/xyz`.

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer <KEY 9ROUTER>" -H "Content-Type: application/json" \
  -d '{"model":"...","messages":[{"role":"user","content":"berapa harga yang pas untuk produk saya"}]}'
```

Untuk `POST /v1/chat/completions`:
1. Ambil pesan user terakhir (teks biasa atau array bagian; gambar diabaikan).
2. Pilih skill (BM25, embedding bila aktif, dan pemilih cadangan bila skor ambigu). Bila tidak ada yang cocok, request diteruskan tanpa skill (tambahan token nol).
3. Isi `SKILL.md` (tanpa frontmatter) dibungkus `<skill name="...">...</skill>`, lalu disusul context pack aktif (`<context_pack>`) dan catatan memori yang cocok (`<memory_notes>`). Urutannya selalu skill -> context pack -> catatan, dan semuanya ditaruh di **awal** system prompt. Bila belum ada pesan system, dibuat satu di paling depan. Context pack dan catatan tetap disisipkan walau tidak ada skill yang terpilih.
4. Request diteruskan ke 9Router, respons (termasuk SSE) dialirkan balik tanpa ditampung.

Catatan:
- Header `Authorization` dari client diteruskan apa adanya. Middleware tidak menyimpan atau memakai key sendiri, dan tidak ada autentikasi/role di middleware; 9Router yang memeriksa key.
- Hanya field `messages` yang diubah, ditambah `"stream": false` bila client tidak mengirim field `stream`. Standar OpenAI menetapkan bawaannya false, tetapi 9Router menjawab SSE bila field itu tidak ada, sehingga SDK OpenAI yang menunggu JSON akan gagal. Field lain di body, termasuk yang tidak dikenal, dibiarkan.
- Header client (termasuk `User-Agent`) diteruskan apa adanya. 9Router berada di belakang Cloudflare yang memblokir User-Agent bawaan Python (`Python-urllib`) dengan 403; client Python sebaiknya memakai SDK OpenAI atau mengisi User-Agent sendiri.
- Respons diberi header `X-Skill: <nama>` bila ada skill yang disisipkan.
- Gagal terbuka: bila langkah skill gagal (body bukan JSON, file skill hilang), request tetap diteruskan tanpa skill dan masalahnya dicatat di log.
- Body maksimal 20 MB (413 bila lebih). Error dari proxy sendiri (413, 502, 504) berbentuk `{"error":{"message","type"}}` agar terbaca SDK OpenAI.
- Skill yang isinya memerintahkan menjalankan script (mis. `python3 ...`) tetap disisipkan apa adanya; model tidak bisa menjalankannya. Pembersihan isi ada di Tahap 2.

## Pemilihan Skill (BM25)

Skill dipilih tanpa LLM: BM25 pada gabungan `name` (bobot 3), `keywords` (bobot 2), dan `description`. Hanya satu skill teratas yang dipilih, dan hanya bila skornya mencapai `SKILL_MIN_SCORE` (default `2.0`). Sapaan dan ucapan terima kasih ("halo", "terima kasih") tidak menghasilkan skor, jadi tidak mendapat skill.

```bash
curl "http://localhost:4000/api/skills/match?q=berapa+harga+yang+pas+untuk+produk+saya"
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

Dua cache di Redis, dengan key berawalan `skillmw:`. Isinya hanya nama folder skill dan skor; key berupa hash, tidak memuat isi pesan atau header `Authorization`.

| Cache | Isi | Masa berlaku |
|---|---|---|
| **Sesi** | skill aktif per percakapan | `SKILL_SESSION_TTL` (45 menit), diperpanjang tiap dipakai |
| **Hasil seleksi** | pesan (dinormalisasi) -> skill terpilih, termasuk "tidak ada yang cocok" | `SKILL_SELECTION_TTL` (1 jam), dan otomatis tidak berlaku saat index dibangun ulang |

**Cache sesi.** Isi skill harus dikirim ulang di setiap request karena model tidak menyimpan apa pun. Dengan cache sesi, pesan lanjutan seperti "oke lanjut" atau "terima kasih" tetap memakai skill yang sama. Skill aktif baru diganti bila skill lain **jauh lebih cocok**: skornya minimal `SKILL_SWITCH_RATIO` (1.5) kali skor skill aktif untuk pesan yang sama.

**Mengenali percakapan.** Format OpenAI tidak punya ID percakapan, jadi dipakai header `X-Conversation-Id` bila client mengirimnya, atau pesan user pertama (yang selalu ikut terkirim). Header `Authorization` ikut di-hash supaya percakapan dua pengguna berbeda tidak bertabrakan. Dua percakapan dari pengguna yang sama yang dimulai dengan pesan identik dianggap satu percakapan, kecuali client mengirim `X-Conversation-Id`.

**Cache hasil seleksi.** Pesan yang sama setelah normalisasi (huruf kecil, tanpa kata umum, urutan kata diabaikan) memakai hasil sebelumnya. Dengan BM25 yang hanya butuh mikrodetik, manfaatnya saat ini kecil; cache ini disiapkan untuk Tahap 3 saat pemilihan memakai embedding yang jauh lebih mahal.

**Redis wajib.** `REDIS_ADDR` harus diisi dan Redis harus terjangkau saat start; bila tidak, server berhenti (seperti PostgreSQL). Setelah server berjalan, Redis yang mati di tengah jalan **tidak** menghentikan request: cache dilewati (status cache tercatat `off`) dan bekerja lagi sendiri setelah Redis pulih. Operasi Redis memakai timeout pendek (200 ms) supaya Redis yang lambat tidak memperlambat request.

## Pemilih Cadangan

Seleksi utama tetap tanpa LLM. Model 9Router (`SKILL_FALLBACK_MODEL`) hanya dipanggil saat skor **ambigu**:
- dua skill atau lebih bersaing ketat (skor BM25 >= `SKILL_AMBIGUOUS_RATIO` x skor teratas, bawaan 0.8), atau
- skor teratas sedikit di bawah threshold (antara 50% dan 100% dari `SKILL_MIN_SCORE`).

Model diberi pesan user dan maksimal 3 kandidat, lalu hanya boleh menjawab satu nama kandidat atau `none`. Jawaban lain dianggap gagal, dan seleksi kembali ke keputusan berdasarkan skor. Keputusannya ikut disimpan di cache hasil seleksi, jadi pesan yang sama tidak memanggil model lagi. Hasil yang gagal (timeout, jawaban tak dikenali) tidak di-cache.

Hasil uji pada 9Router asli dengan `AI-Crypro`: tiga pesan uji dipilih benar (harga -> `founder-pricing`, pesaing -> `founder-competitors`, sapaan -> `none`), sekitar **2,3-2,9 detik dan sekitar 2.200 token per panggilan**. Token sebesar itu sebagian besar berasal dari prompt tambahan di sisi 9Router/provider, bukan dari prompt middleware (sekitar 200 token); Gemini Flash menunjukkan angka serupa. Artinya setiap pesan ambigu menambah beberapa detik ke request user. `SKILL_FALLBACK_TIMEOUT` (bawaan 5 detik) membatasi tunggu maksimalnya. Model `kr/*` di 9Router saat ini menolak request ("This version of Kiro is no longer supported"), jadi jangan dipakai.

Untuk melihat apakah sebuah pesan akan memicu pemilih cadangan: `GET /api/skills/match?q=...` (field `ambiguous`), atau tambah `&fallback=true` untuk benar-benar menjalankannya.

## Embedding

Embedding memilih skill berdasarkan **makna**, jadi pesan Indonesia bisa cocok dengan deskripsi skill berbahasa Inggris tanpa `keywords`. Embedding **dimatikan selama `EMBEDDING_MODEL` kosong** (seleksi memakai BM25). Saat ini 9Router belum punya provider embedding (`/v1/embeddings` menjawab "No credentials for provider: openai"), jadi bagian ini siap tetapi belum aktif.

Cara mengaktifkan, salah satu:
- tambahkan provider embedding (mis. OpenAI) di dashboard 9Router, lalu isi `EMBEDDING_MODEL` dengan nama modelnya; alamat dan key mengikuti `AI_ROUTER_*`;
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
  -d '{"skill":"founder-pricing"}'
```

Tanpa `variants`, yang dibandingkan: `full` (SKILL.md), `short` (SHORT.md, bila ada), dan `none` (tanpa skill, sebagai pembanding). Opsional: `model` (model yang diuji, bawaan `EVAL_TARGET_MODEL`) dan `limit` (jumlah pertanyaan). Eval berjalan di latar belakang dan langsung mengembalikan `run_id`.

**3. Lihat hasil:** `GET /api/evals/runs/<run_id>` berisi skor rata-rata per varian, rata-rata token skill dan token prompt, jumlah yang gagal, serta kesimpulan otomatis, misalnya "SHORT.md setara dengan SKILL.md ... layak dipakai" atau "Skill hampir tidak menaikkan skor dibanding tanpa skill". Tambah `?detail=true` untuk melihat setiap jawaban, skor, dan alasan hakim.

Catatan:
- Hakim (`EVAL_JUDGE_MODEL`) menilai 0-10 berdasarkan `criteria`, **tanpa diberi tahu varian mana** yang menjawab, supaya tidak berat sebelah.
- Setiap baris menyimpan `skill_version` (hash SKILL.md + SHORT.md), jadi hasil sebelum dan sesudah skill diedit bisa dibandingkan.
- Biaya: jumlah pertanyaan x jumlah varian x 2 panggilan model (menjawab + menilai). Dengan 20 pertanyaan dan 3 varian = 120 panggilan, dan pada 9Router asli sekitar 2.200 token per panggilan. Jalankan saat skill berubah, bukan terus-menerus.
- Hasil tersimpan di tabel `t_eval_runs`. Eval yang sedang berjalan saat server di-restart berhenti di tengah; hasil yang sudah selesai tetap tersimpan.

## Memori (Context Pack dan Catatan)

Tahap 4 menambahkan dua jenis memori. Keduanya **global** (tidak terikat user) dan disimpan di PostgreSQL.

**1. Context pack (`m_context_packs`)**: ringkasan tetap yang selalu relevan, misalnya gambaran bisnis, istilah, atau aturan yang berlaku. Semua context pack yang aktif disisipkan di setiap request setelah skill, diurutkan menurut nama, dan dibatasi `CONTEXT_PACK_MAX_TOKENS` (dipotong di batas paragraf). Teksnya di-cache di memori selama 30 detik, dan perubahan lewat API langsung mengosongkan cache. Bila database gagal dibaca, teks terakhir tetap dipakai.

```bash
curl -X POST http://localhost:4000/api/context-packs -H "Content-Type: application/json" \
  -d '{"name":"bisnis","content":"Kedai kopi di Bandung, jual kopi susu literan via Instagram dan GoFood. Target: mahasiswa."}'
```

**2. Catatan memori (`t_memory_notes`)**: pengalaman yang pernah dicoba beserta hasilnya, dengan bentuk *topik, yang dicoba, hasil, sumber hasil (`user`/`data`), pelajaran*. Siklusnya:

1. **Ekstraksi otomatis.** Setelah respons chat selesai, pesan user terakhir diperiksa. Bila memuat sinyal hasil (berhasil, gagal, omzet, laku, naik, turun, untung, rugi, terjual, ...), model `MEMORY_EXTRACT_MODEL` mengekstraknya di latar belakang menjadi catatan berstatus `proposed`. Model hanya boleh mencatat hal yang sudah dicoba dan hasilnya sudah terjadi, bukan rencana atau saran asisten. Pesan tanpa sinyal tidak memanggil model sama sekali. Pesan yang sama (misalnya request diulang client) tidak diekstrak dua kali.
2. **Konfirmasi lewat API.** Catatan usulan tidak dipakai sampai dikonfirmasi (`POST /:id/confirm` -> `active`), dikoreksi (`PUT /:id` -> `corrected`, langsung dipakai), ditolak (`POST /:id/reject`), atau dihapus. Catatan yang ditolak atau dihapus tetap disimpan barisnya supaya isi yang sama tidak diusulkan lagi. Catatan manual (`POST /api/memory-notes`) langsung aktif.
3. **Penyisipan.** Untuk setiap pesan, catatan `active`/`corrected` yang belum kedaluwarsa dicari dengan pencarian teks PostgreSQL (kolom `search_vector` dan index GIN, konfigurasi `simple`). Yang dipakai hanya catatan yang memuat paling sedikit `MEMORY_MIN_MATCH` kata pesan, maksimal `MEMORY_MAX_NOTES`, dan masing-masing dibatasi `MEMORY_NOTE_MAX_TOKENS`. Model diminta memperlakukannya sebagai **pengingat yang perlu dicek ulang, bukan larangan**: bila rencana user mengulang pola yang dulu gagal, model menyebutkannya lebih dulu lalu menawarkan alternatif.
4. **Kedaluwarsa.** Catatan berlaku `MEMORY_NOTE_TTL` sejak dikonfirmasi/dikoreksi (bawaan 90 hari), karena hasil bisnis bisa basi.

```bash
# catatan usulan yang menunggu konfirmasi
curl "http://localhost:4000/api/memory-notes?status=proposed"
# catatan apa yang akan ikut untuk sebuah pesan
curl "http://localhost:4000/api/memory-notes/match?q=mau+promo+diskon+50%25+lagi"
```

Contoh hasil uji nyata: setelah ada catatan "promo diskon 50% kopi susu: penjualan naik 2x tapi rugi Rp1,2 juta", pertanyaan "aku mau bikin promo diskon 50% lagi" dijawab model dengan menyebut hasil itu lebih dulu, lalu menyarankan bundling atau diskon 15-20%.

Catatan:
- PostgreSQL tidak punya kamus bahasa Indonesia, jadi akhiran kata ditangani dengan pencarian prefix (`diskon:*` juga cocok dengan "diskonnya"). Hasilnya lalu disaring lagi dengan tokenizer yang sama dengan seleksi skill.
- Pencarian catatan dibatasi `MEMORY_LOOKUP_TIMEOUT`. Bila lewat atau database gagal, request jalan tanpa catatan (gagal terbuka).
- Ekstraksi yang berjalan bersamaan dibatasi `MEMORY_EXTRACT_CONCURRENCY`. Bila antrean penuh, ekstraksi dilewati. Biaya token ekstraksi dicatat di log aplikasi (`ekstraksi memori selesai`).
- `MEMORY_EXTRACT_MODEL` kosong = ekstraksi mati, tetapi catatan manual dan penyisipan tetap berjalan. `MEMORY_MAX_NOTES=0` mematikan penyisipan catatan.

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
- Yang didukung hanya server MCP HTTP (Streamable HTTP), bukan server lokal `npx`/stdio.

**2. Login sekali per server (OAuth)**: buka `http://<BASE_URL>/api/mcp/servers/canva/connect` di browser, lalu login dan setujui akses. Middleware otomatis mencari alamat login, mendaftarkan dirinya sebagai aplikasi OAuth, dan memakai PKCE. Akun yang dipakai adalah **satu akun bersama** untuk semua chat. Token disimpan terenkripsi (`MCP_TOKEN_KEY`) di `m_mcp_credentials` dan diperpanjang otomatis. `BASE_URL` harus bisa dibuka dari browser, karena halaman login kembali ke `BASE_URL/api/mcp/oauth/callback`.

**3. Setelah itu berjalan sendiri di setiap chat:**
- Tool dipilih **otomatis sesuai pesan** (BM25 pada nama server, `keywords`, nama dan deskripsi tool, dari dua pesan user terakhir). Maksimal `MCP_MAX_TOOLS` tool dengan skor ≥ `MCP_TOOL_MIN_SCORE`.
- Pesan yang tidak menyinggung tool apa pun ("halo") diteruskan biasa tanpa tool dan tanpa tambahan token.
- Bila model meminta tool, middleware menjalankannya (bersamaan, batas `MCP_TOOL_TIMEOUT`), memberikan hasilnya ke model, dan mengulanginya sampai ada jawaban akhir (maks `MCP_MAX_STEPS` putaran).
- **Semua tool diizinkan**, termasuk yang mengubah data. Pakai akun yang aman untuk diubah.
- Bila client sudah membawa `tools` sendiri, tool MCP tidak ditambahkan.

```bash
curl http://localhost:4000/api/mcp/servers                                        # status + link login
curl "http://localhost:4000/api/mcp/tools?q=buatkan+poster+promo+di+canva"        # tool yang akan terpilih
```

Catatan:
- Chat yang memakai tool kira-kira **2x lebih lambat dan mahal**, karena model dipanggil minimal dua kali. Hasil uji: sekitar 7,5 detik dan sekitar 5.500 token prompt. Usage di respons adalah jumlah semua putaran.
- Untuk client streaming, jawaban dikirim **sekaligus di akhir** (bukan per kata). Selama tool berjalan, client menerima komentar SSE `: keepalive` supaya koneksi tidak putus.
- Tool yang gagal tidak menggagalkan chat: model menerima `ERROR: ...` dan menjelaskannya. Hasil berupa gambar hanya disebutkan, tidak bisa dilihat model.
- **Figma:** server MCP resminya menolak pendaftaran klien (`403`, hanya aplikasi yang disetujui Figma), jadi `figma` di `mcp.json` diset `enabled: false`. Alternatifnya, jalankan server MCP Figma pihak ketiga yang memakai personal access token sebagai layanan HTTP terpisah.
- Respons yang membawa tool diberi header `X-MCP-Tools`. Pemakaian tool dicatat di `t_usage_logs` (`mcp_tools_offered`, `mcp_tool_calls`, `mcp_tool_names`, `mcp_steps`) dan diringkas di bagian `mcp` pada `/api/usage-logs/summary`.

Penjelasan lengkap: [`docs/10-mcp.md`](docs/10-mcp.md).

## Index Skill

Saat start, middleware membaca `skills/*/SKILL.md` dan menyimpan `name`, `description`, `keywords`, serta ada tidaknya `SHORT.md` di memori. Isi skill tidak disimpan di index; dibaca dari disk saat skill terpilih.

- Index dibangun ulang otomatis setelah upload sukses, dan oleh **watcher** yang memeriksa folder `skills/` tiap `SKILL_WATCH_INTERVAL` (5 detik; 0 = dimatikan). File yang ditambah, diubah, atau dihapus langsung di disk terbaca otomatis. Perubahan baru dimuat setelah stabil dua kali pemeriksaan berturut-turut, supaya file yang sedang ditulis tidak terbaca setengah.
- Index baru dibangun terpisah lalu ditukar dalam satu langkah atomik, jadi request yang sedang berjalan tidak pernah melihat index setengah jadi.
- Folder tanpa `SKILL.md`, dengan frontmatter rusak, atau `description` kosong dilewati dan dicatat di log, tanpa menggagalkan skill lain.
- Folder berawalan `.` (staging dan backup upload) diabaikan.

## Daftar Skill

```bash
curl http://localhost:4000/api/skills
```

Mengembalikan array di `data`, terurut menurut nama: `name`, `description` (dari frontmatter), `files`, `has_short`, `size_bytes`, dan `updated_at` (waktu ubah file terbaru). Folder berawalan `.` (staging/backup upload) tidak ikut. Skill yang `SKILL.md`-nya hilang atau frontmatter-nya rusak tetap muncul dengan field `error`.

## Upload Skill (zip)

```bash
curl -X POST http://localhost:4000/api/skills/upload \
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
- Endpoint ini tidak memakai autentikasi.

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
  (request log ke zap), `CORS`.
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
