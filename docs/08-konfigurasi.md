# 8. Konfigurasi (`.env`)

- Nilai asli dan rahasia (password, key) **hanya** di `.env`. File ini tidak di-commit.
- `.env.example` hanya berisi contoh/placeholder dan di-commit sebagai panduan.
- Setiap variabel baru harus ditambahkan ke **kedua file** dengan komentar singkat berbahasa Indonesia.
- Durasi ditulis seperti `200ms`, `2s`, `45m`, `1h`, atau `2160h`.

## Aplikasi

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `APP_ENV` | `development` | `production` mematikan log berwarna di console |
| `GIN_MODE` | `debug` | `release` untuk produksi |
| `PORT` | `4000` | Port HTTP |
| `BASE_URL` | `http://localhost:4000` | Alamat publik server |
| `PATH_FILE_UPLOAD` | `skills` | Folder skill (dibuat otomatis) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_DIR` | `logs` | Folder file log |

## Database (wajib)

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `DB_HOST`, `DB_PORT` | `localhost`, `5432` | Alamat PostgreSQL (versi 13+ untuk `gen_random_uuid()`) |
| `DB_NAME`, `DB_USER`, `DB_PASSWORD` | - | Nama database, user, password |
| `DB_SSLMODE` | `disable` | |
| `DB_TIMEZONE` | `Asia/Jakarta` | |

## Redis (wajib)

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `REDIS_ADDR` | - | **Wajib.** Server berhenti bila kosong atau tidak terjangkau saat start |
| `REDIS_PASSWORD` | kosong | |
| `REDIS_DB` | `0` | |
| `REDIS_TIMEOUT` | `200ms` | Batas waktu tiap operasi. Pakai `2s` bila Redis jauh, misalnya Redis DEV di VPS dari komputer lokal |

## 9Router (wajib)

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `AI_ROUTER_BASE_URL` | - | Contoh `https://host/v1`. Path `/v1/xyz` diteruskan ke `<BASE_URL>/xyz` |
| `AI_ROUTER_API_KEY` | - | Key milik middleware, hanya untuk panggilannya sendiri (pemilih cadangan, eval, ekstraksi). Request client memakai key client. |
| `AI_ROUTER_MODEL` | - | Model bawaan untuk eval bila `EVAL_*_MODEL` kosong |

## Pemilihan skill

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `SKILL_MIN_SCORE` | `2.0` | Threshold skor BM25 |
| `SKILL_SWITCH_RATIO` | `1.5` | Skill aktif diganti bila skill lain skornya ≥ rasio ini × skor skill aktif |
| `SKILL_AMBIGUOUS_RATIO` | `0.8` | Skill lain dianggap bersaing bila skornya ≥ rasio ini × skor teratas |
| `SKILL_FALLBACK_MODEL` | kosong | Model pemilih cadangan; kosong = dimatikan |
| `SKILL_FALLBACK_TIMEOUT` | `5s` | |
| `SKILL_BUDGET_DEFAULT` | `3000` | Budget token skill (0 = tanpa batas) |
| `SKILL_BUDGETS` | kosong | `pola=token,...`, contoh `*mini*=800,*flash*=800` |
| `SKILL_SESSION_TTL` | `45m` | Masa berlaku skill aktif per percakapan |
| `SKILL_SELECTION_TTL` | `1h` | Masa berlaku cache hasil seleksi |
| `SKILL_WATCH_INTERVAL` | `5s` | Interval pemeriksaan folder skill (0 = watcher mati) |

## Embedding (opsional)

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `EMBEDDING_MODEL` | kosong | Kosong = embedding mati, seleksi memakai BM25 |
| `EMBEDDING_BASE_URL` | `AI_ROUTER_BASE_URL` | Contoh Ollama: `http://localhost:11434/v1` |
| `EMBEDDING_API_KEY` | `AI_ROUTER_API_KEY` | |
| `EMBEDDING_TIMEOUT` | `2s` | Batas embedding pesan per request |
| `SKILL_MIN_SIMILARITY` | `0.5` | Threshold kemiripan (-1..1) |
| `SKILL_SWITCH_MARGIN` | `0.05` | Selisih kemiripan untuk mengganti skill aktif |
| `SKILL_AMBIGUOUS_MARGIN` | `0.03` | Selisih kemiripan yang dianggap bersaing |

## Eval

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `EVAL_TARGET_MODEL` | `AI_ROUTER_MODEL` | Model yang diuji |
| `EVAL_JUDGE_MODEL` | `AI_ROUTER_MODEL` | Model hakim |
| `EVAL_CONCURRENCY` | `3` | Soal yang diproses bersamaan |

## Memori

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `CONTEXT_PACK_MAX_TOKENS` | `800` | Batas token gabungan context pack aktif (0 = tanpa batas) |
| `MEMORY_MAX_NOTES` | `2` | Catatan maksimal per request (0 = penyisipan catatan mati) |
| `MEMORY_NOTE_MAX_TOKENS` | `150` | Batas token per catatan |
| `MEMORY_MIN_MATCH` | `2` | Kata pesan minimal yang harus ada di catatan |
| `MEMORY_LOOKUP_TIMEOUT` | `300ms` | Batas waktu pencarian catatan |
| `MEMORY_NOTE_TTL` | `2160h` (90 hari) | Masa berlaku sejak dikonfirmasi (0 = selamanya) |
| `MEMORY_EXTRACT_MODEL` | kosong | Model pengekstrak; kosong = ekstraksi mati |
| `MEMORY_EXTRACT_TIMEOUT` | `60s` | Batas waktu satu ekstraksi |
| `MEMORY_EXTRACT_CONCURRENCY` | `2` | Ekstraksi yang boleh berjalan bersamaan |

## MCP

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `MCP_CONFIG_PATH` | `mcp.json` | File daftar server MCP. Tidak ada = MCP mati. |
| `MCP_TOKEN_KEY` | - | Kunci enkripsi token login (64 karakter hex, `openssl rand -hex 32`). **Wajib** bila ada server aktif. Jangan diganti setelah login. |
| `MCP_MAX_TOOLS` | `8` | Tool maksimal per request (0 = tool tidak dipakai di chat) |
| `MCP_TOOL_MIN_SCORE` | `1.0` | Skor BM25 minimal sebuah tool supaya dikirim |
| `MCP_MAX_STEPS` | `6` | Putaran panggil-tool maksimal per request |
| `MCP_TOOL_TIMEOUT` | `60s` | Batas waktu satu pemanggilan tool |
| `MCP_TOOL_RESULT_MAX_TOKENS` | `4000` | Batas token hasil satu tool yang diberikan ke model |
| `MCP_REFRESH_INTERVAL` | `30m` | Pengambilan ulang daftar tool (0 = hanya saat start dan login) |

`BASE_URL` juga dipakai MCP sebagai alamat balik login OAuth, jadi harus bisa dibuka dari browser.

## Docker (hanya dipakai `docker-compose.yml`)

| Variabel | Bawaan | Keterangan |
|---|---|---|
| `DOCKER_PORT_BIND` | `127.0.0.1:4000` | Alamat:port di VPS tempat container dibuka. Bawaan hanya dari VPS sendiri, karena `/api/*` tanpa autentikasi. |
| `DOCKER_IMAGE` | `middleware-skill-ai:latest` | Nama image; isi dengan image dari registry bila dibangun CI/CD |

Di dalam container, compose selalu memaksa `APP_ENV=production`, `GIN_MODE=release`, `PORT=4000`, `PATH_FILE_UPLOAD=/app/skills`, `LOG_DIR=/app/logs`, dan `MCP_CONFIG_PATH=/app/mcp.json` (file `mcp.json` di folder proyek dipasang read-only).

## Pilihan model saat ini

| Fungsi | Model |
|---|---|
| Pemilih cadangan, hakim eval, model eval, ekstraksi memori | `AI-Crypro` |
| Embedding | belum ada (9Router belum punya provider embedding) |

Jangan memakai model `kr/*`, karena saat ini selalu ditolak 9Router.
