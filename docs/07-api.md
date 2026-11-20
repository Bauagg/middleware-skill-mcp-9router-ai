# 7. API

Alamat bawaan: `http://localhost:4000`. Semua endpoint `/api/*` **tanpa autentikasi**.

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
  -H "Authorization: Bearer <KEY 9ROUTER>" -H "Content-Type: application/json" \
  -H "X-Conversation-Id: percakapan-123" \
  -d '{"model":"AI-Crypro","messages":[{"role":"user","content":"berapa harga yang pas untuk produk saya"}]}'
```

- `Authorization` wajib berisi key 9Router milik client. Middleware meneruskannya apa adanya.
- `X-Conversation-Id` opsional tapi disarankan, supaya skill aktif per percakapan terjaga dengan benar.
- Respons membawa header `X-Skill: <nama>` bila ada skill yang disisipkan, dan `X-MCP-Tools: <jumlah>` bila tool MCP ikut dikirim ke model.
- Error dari middleware sendiri: 413 (body > 20 MB), 502 (9Router tidak terjangkau), 504 (timeout).

## Skill

| Method | URL | Keterangan |
|---|---|---|
| GET | `/api/skills` | Daftar skill terpasang: `name`, `description`, `files`, `has_short`, `size_bytes`, `updated_at` (dan `error` bila rusak) |
| POST | `/api/skills/upload` | Upload zip (form `file`, opsional `overwrite=true`) |
| GET | `/api/skills/match?q=&limit=5&fallback=false` | Skill yang akan terpilih untuk sebuah pesan, skor semua kandidat, dan apakah ambigu |
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
| GET | `/api/mcp/servers` | Status semua server di `mcp.json`: `state`, jumlah tool, sudah login atau belum, `connect_url` |
| GET | `/api/mcp/servers/:name/connect` | Buka di **browser**: mengarahkan ke halaman login server (OAuth). `?redirect=false` mengembalikan `auth_url` sebagai JSON. |
| GET | `/api/mcp/oauth/callback` | Tujuan balik setelah login (dipanggil browser, membalas halaman HTML) |
| POST | `/api/mcp/servers/:name/refresh` | Ambil ulang daftar tool server |
| DELETE | `/api/mcp/servers/:name/connection` | Hapus token login server |
| GET | `/api/mcp/tools?q=&limit=` | Semua tool yang terhubung. Dengan `q`: skor tiap tool dan mana yang `selected` untuk pesan itu. |

## Log pemakaian

| Method | URL | Keterangan |
|---|---|---|
| GET | `/api/usage-logs?page=&limit=&model=&skill=` | Daftar log, terbaru lebih dulu |
| GET | `/api/usage-logs/summary?from=&to=` | Ringkasan. `from`/`to` berformat `YYYY-MM-DD` atau RFC3339; `to` tidak ikut dihitung |

## Eval

| Method | URL | Keterangan |
|---|---|---|
| POST | `/api/evals/runs` | Mulai eval (202): `{"skill", "variants", "model", "limit"}` |
| GET | `/api/evals/runs?skill=&limit=` | Daftar eval terbaru |
| GET | `/api/evals/runs/:id?detail=true` | Hasil per varian, kesimpulan, dan (dengan `detail`) tiap jawaban |
