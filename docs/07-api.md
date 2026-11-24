# 7. API

Alamat bawaan: `http://localhost:4000`. Semua endpoint dijaga secret key (`X-API-Key`) **atau** token login (`Authorization: Bearer`, lihat [Akun dan login](12-akun-dan-login.md)), data per user dipisah lewat `X-User-Id`, dan `/v1/*` memakai 9Router milik user (`X-Router-URL` + `X-Router-Key`).

## Secret key aplikasi: header `X-API-Key`

Setiap request dari aplikasi pihak ketiga wajib membawa header **`X-API-Key`** berisi salah satu key di `API_KEYS` (`.env`). User yang login di middleware memakai `Authorization: Bearer <access_token>` sebagai gantinya. Tanpa keduanya, atau nilainya salah, dijawab **401**: format amplop biasa untuk `/api/*`, format error OpenAI (`invalid_api_key`) untuk `/v1/*`.

Yang tidak butuh key, karena dibuka langsung oleh browser yang tidak bisa mengirim header:
- `GET /` dan `GET /health`;
- `POST /api/auth/register`, `/api/auth/login`, `/api/auth/refresh`;
- `GET /images/:file` (link gambar di jawaban chat);
- `GET /api/mcp/oauth/callback` dan link login MCP bertanda tangan (`/api/mcp/servers/:name/connect?u=...&sig=...`).

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "X-API-Key: <API_KEYS>" -H "X-Router-URL: https://<domain-9router>/v1" -H "X-Router-Key: <KEY 9ROUTER MILIK USER>" -H "X-User-Id: user-123" \
  -H "Content-Type: application/json" \
  -d '{"model":"AI-Crypro","messages":[{"role":"user","content":"halo"}]}'
```

Key sebaiknya dipakai dari **server aplikasi** (server-to-server). Bila dikirim dari aplikasi web di browser, key bisa dilihat siapa pun lewat developer tools.

## 9Router milik user: header `X-Router-URL`, `X-Router-Key`, dan field `model`

Middleware adalah alat di depan 9Router milik user, bukan pemilik akun 9Router. Setiap user memakai key dan model 9Router miliknya sendiri:

| Yang dikirim | Di mana | Bila tidak ada |
|---|---|---|
| Alamat 9Router (mis. `https://domain-9router/v1`) | header **`X-Router-URL`**, wajib untuk semua `/v1/*` | **400** `invalid_request_error` (juga bila bukan http/https atau berisi user:password/query) |
| Key 9Router | header **`X-Router-Key`**, wajib untuk semua `/v1/*` | **401** `invalid_api_key` |
| Model 9Router | field **`model`** di body `POST /v1/chat/completions` | **400** `invalid_request_error` |

Key itu dipakai untuk **semua** panggilan ke 9Router selama chat tersebut: jawaban, putaran tool, buat gambar, pemilih cadangan skill, dan ekstraksi memori di latar belakang. Biayanya masuk ke akun 9Router user. Header `Authorization` dari client diabaikan, dan `X-API-Key`, `X-Router-URL`, `X-Router-Key`, `X-User-Id` tidak ikut diteruskan ke 9Router. Alamat yang mengarah ke jaringan internal (localhost, IP privat, `169.254.169.254`) ditolak dengan 502, kecuali `ROUTER_ALLOW_PRIVATE_NETWORK=true`. Middleware tidak punya key atau model 9Router sendiri di `.env`. Model untuk pemilih cadangan dan ekstraksi memori sama dengan `model` chat, dan model gambar diambil dari akun user (`GET /v1/models/image`).

## Identitas user: header `X-User-Id`

Aplikasi pihak ketiga (mis. Luma) mengirim identitas user di header **`X-User-Id`** (huruf, angka, `. _ @ : -`, maks 128). User yang login di middleware tidak perlu header ini: `user_id`-nya diambil dari token, dan header `X-User-Id` diabaikan. Semua data per user dipisah berdasarkan `user_id`.

**Role:** admin (pemilik aplikasi, lihat [Akun dan login](12-akun-dan-login.md)) melihat dan mengelola data **semua** user di endpoint `/api/*`. User biasa, termasuk semua user dari pihak ketiga, hanya melihat miliknya. Skill selalu global.

| Endpoint | `X-User-Id` |
|---|---|
| `POST /v1/chat/completions` | **wajib** (400 bila tidak ada) |
| `/api/context-packs`, `/api/memory-notes`, `/api/images`, `/api/mcp/*` (kecuali di bawah) | **wajib**, data milik user itu (admin: semua) |
| `/api/usage-logs*`, `/api/evals/*` | **wajib**, data milik user itu (admin: semua, log bisa difilter `?user_id=`) |
| `GET /api/mcp/admin/servers` | **khusus admin** (403 untuk lainnya): server MCP tambahan dan status login semua user |
| `/api/mcp/servers/:name/connect` | header/token **atau** link bertanda tangan dari chat (pada link, user hanya diambil dari tanda tangan) |
| `/api/mcp/oauth/callback`, `/images/:file`, `/api/skills*` | tidak perlu (skill berlaku untuk semua user) |

## Bentuk respons `/api/*`

```json
{ "status": "success", "message": "Berhasil ...", "data": { } }
```

```json
{ "status": "fail", "message": "Validasi gagal", "errors": [ { "field": "id", "message": "id harus UUID" } ] }
```

Daftar berhalaman menambahkan `meta`: `{ "page", "limit", "total", "total_pages" }`.

| Kode | Arti |
|---|---|
| 200 / 201 / 202 | berhasil / dibuat / diterima (diproses di latar belakang) |
| 400 | input salah, misalnya `:id` bukan UUID atau perubahan status yang tidak boleh |
| 404 | data tidak ditemukan |
| 409 | data ganda, misalnya nama context pack atau isi catatan yang sama |
| 500 | kesalahan server |

Endpoint `/v1/*` memakai format error OpenAI: `{"error": {"message", "type"}}`.

## Akun

| Method | URL | Keterangan |
|---|---|---|
| POST | `/api/auth/register` | `{"email","password"}` → akun (role `user`) + token. 409 bila email terdaftar |
| POST | `/api/auth/login` | `{"email","password"}` → akun + token |
| POST | `/api/auth/refresh` | `{"refresh_token"}` → pasangan token baru |
| GET | `/api/auth/me` | Akun yang sedang login (butuh access token) |

Detail: [Akun, login, dan role](12-akun-dan-login.md).

## Umum

| Method | URL | Keterangan |
|---|---|---|
| GET | `/` | `Server is running` |
| GET | `/health` | `{ "status": "ok" }` |
| GET | `/api/ping` | `{ "message": "pong" }` |

## Proxy ke 9Router

| Method | URL | Keterangan |
|---|---|---|
| POST | `/v1/chat/completions` | Disisipi skill, context pack, dan catatan memori, lalu diteruskan |
| ANY | `/v1/*` | Diteruskan polos, misalnya `/v1/models` |

```bash
curl http://localhost:4000/v1/chat/completions \
  -H "X-API-Key: <API_KEYS>" -H "X-Router-URL: https://<domain-9router>/v1" -H "X-Router-Key: <KEY 9ROUTER MILIK USER>" -H "X-User-Id: user-123" \
  -H "Content-Type: application/json" -H "X-Conversation-Id: percakapan-123" \
  -d '{"model":"AI-Crypro","messages":[{"role":"user","content":"berapa harga yang pas untuk produk saya"}]}'
```

- `X-User-Id` wajib: identitas user dari aplikasi client.
- `X-Router-URL` wajib: alamat 9Router milik user (400 bila tidak ada).
- `X-Router-Key` wajib: key 9Router milik user (401 bila tidak ada). `Authorization` tidak perlu dan diabaikan.
- `model` wajib: model 9Router milik user (400 bila tidak ada). Model gambar diambil otomatis dari akun 9Router user.
- `X-Conversation-Id` opsional tapi disarankan, supaya skill aktif per percakapan terjaga dengan benar.
- Respons membawa header `X-Skill: <nama>` bila ada skill yang disisipkan, dan `X-MCP-Tools: <jumlah>` bila tool MCP ikut dikirim ke model.
- Error dari middleware sendiri: 413 (body > 20 MB), 502 (9Router tidak terjangkau), 504 (timeout).

## Skill

| Method | URL | Keterangan |
|---|---|---|
| GET | `/api/skills` | Daftar skill terpasang: `name`, `description`, `files`, `has_short`, `size_bytes`, `updated_at` (dan `error` bila rusak) |
| POST | `/api/skills/upload` | Upload zip (form `file`, opsional `overwrite=true`) |
| GET | `/api/skills/export?names=a,b` | Download zip berisi skill yang terpasang (tanpa `names` = semua). Link ini juga diberikan chat setelah impor dari GitHub. |
| GET | `/api/skills/match?q=&limit=5&fallback=false&model=` | Skill yang akan terpilih untuk sebuah pesan, skor semua kandidat, dan apakah ambigu. `fallback=true` butuh `model`, header `X-Router-URL`, dan header `X-Router-Key`. |
| GET | `/api/skills/preview/:name?model=&budget=` | Teks skill persis seperti yang akan disisipkan |

```bash
curl -X POST http://localhost:4000/api/skills/upload -F "file=@skill.zip" -F "overwrite=true"
curl "http://localhost:4000/api/skills/match?q=cara+menentukan+harga"
curl "http://localhost:4000/api/skills/preview/founder-pricing?model=gpt-4o-mini"
```

## Context pack

| Method | URL | Keterangan |
|---|---|---|
| GET | `/api/context-packs` | Daftar semua context pack |
| POST | `/api/context-packs` | Buat baru: `{"name", "content", "active"}` (`active` bawaan `true`) |
| GET | `/api/context-packs/:id` | Detail |
| PUT | `/api/context-packs/:id` | Ubah; hanya field yang dikirim yang berubah |
| DELETE | `/api/context-packs/:id` | Hapus |

Batas: `name` maks 100 karakter dan unik, `content` maks 20.000 karakter.

## Catatan memori

| Method | URL | Keterangan |
|---|---|---|
| GET | `/api/memory-notes?status=&q=&page=&limit=` | Daftar/cari. `status`: proposed, active, corrected, rejected, deleted. Tanpa `status`, semua kecuali deleted ditampilkan. |
| GET | `/api/memory-notes/match?q=` | Catatan yang akan disisipkan untuk pesan `q`, beserta teks bloknya dan perkiraan token |
| POST | `/api/memory-notes` | Catatan manual (langsung `active`): `{"topic", "tried", "result", "result_source", "lesson"}` |
| GET | `/api/memory-notes/:id` | Detail |
| PUT | `/api/memory-notes/:id` | Koreksi isi; status jadi `corrected` |
| DELETE | `/api/memory-notes/:id` | Hapus; status jadi `deleted` |
| POST | `/api/memory-notes/:id/confirm` | `proposed`/`rejected` → `active` |
| POST | `/api/memory-notes/:id/reject` | `proposed`/`active`/`corrected` → `rejected` |

```bash
curl -X POST http://localhost:4000/api/memory-notes -H "Content-Type: application/json" -d '{
  "topic": "promo diskon kopi susu",
  "tried": "promo diskon 50% kopi susu literan selama seminggu",
  "result": "penjualan naik 2x tapi rugi Rp1,2 juta",
  "result_source": "data",
  "lesson": "diskon besar menggerus margin; coba bundling atau diskon kecil"
}'
```

`result_source` bernilai `user` atau `data`, bawaan `user`.

## MCP

| Method | URL | Keterangan |
|---|---|---|
| GET | `/api/mcp/servers` | Server bawaan + milik user beserta status koneksi user: `state`, `own`, jumlah tool, sudah login, `connect_url` (link login bertanda tangan) |
| POST | `/api/mcp/servers` | Tambah server milik user: `{"name","url","keywords"}` (URL dicek dulu) |
| DELETE | `/api/mcp/servers/:name` | Hapus server milik user beserta login-nya |
| GET | `/api/mcp/servers/:name/connect` | Buka di **browser**: mengarahkan ke halaman login server (OAuth). `?redirect=false` mengembalikan `auth_url` sebagai JSON. |
| GET | `/api/mcp/oauth/callback` | Tujuan balik setelah login (dipanggil browser, membalas halaman HTML) |
| POST | `/api/mcp/servers/:name/refresh` | Ambil ulang daftar tool server |
| DELETE | `/api/mcp/servers/:name/connection` | Hapus token login server |
| GET | `/api/mcp/tools?q=&limit=` | Semua tool yang terhubung. Dengan `q`: skor tiap tool dan mana yang `selected` untuk pesan itu. |

## Gambar

| Method | URL | Keterangan |
|---|---|---|
| GET | `/images/:file` | Gambar hasil `generate_image` (di luar `/api`, link ini yang muncul di jawaban chat). 404 bila nama tidak valid. |
| GET | `/api/images?q=&page=&limit=` | Daftar gambar yang pernah dibuat (tabel `t_generated_images`), terbaru lebih dulu. `q` mencari di prompt dan alt. |
| GET | `/api/images/:id` | Detail satu gambar: `url`, `file_name`, `prompt`, `alt`, `model`, ukuran, waktu |

Gambar dibuat lewat chat, bukan lewat endpoint ini (lihat [Gambar lewat chat](11-gambar.md)). `POST /v1/images/generations` tetap bisa dipanggil langsung dan diteruskan polos ke 9Router.

## Log pemakaian

Filter per user: tambahkan `?user_id=<id>` pada `GET /api/usage-logs` dan `GET /api/usage-logs/summary`.


| Method | URL | Keterangan |
|---|---|---|
| GET | `/api/usage-logs?page=&limit=&model=&skill=` | Daftar log, terbaru lebih dulu |
| GET | `/api/usage-logs/summary?from=&to=` | Ringkasan. `from`/`to` berformat `YYYY-MM-DD` atau RFC3339; `to` tidak ikut dihitung |

## Eval

| Method | URL | Keterangan |
|---|---|---|
| POST | `/api/evals/runs` | Mulai eval (202): `{"skill", "model", "judge_model", "variants", "limit"}`. `model` wajib, header `X-Router-URL` (400) dan `X-Router-Key` (401) wajib. |
| GET | `/api/evals/runs?skill=&limit=` | Daftar eval terbaru |
| GET | `/api/evals/runs/:id?detail=true` | Hasil per varian, kesimpulan, dan (dengan `detail`) tiap jawaban |
