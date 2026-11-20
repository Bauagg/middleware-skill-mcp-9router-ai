# 2. Struktur kode

## Folder

```
middleware-skill-ai/
├── cmd/server/main.go        # titik masuk: config -> logger -> DB -> router -> jalan -> berhenti rapi saat SIGTERM
├── internal/
│   ├── config/               # membaca .env menjadi struct Config
│   ├── logger/               # zap: logs/combined.log, logs/error.log, dan console
│   ├── database/             # koneksi GORM + AutoMigrate
│   ├── middleware/           # middleware HTTP global: recovery, log request, CORS
│   ├── router/router.go      # SATU-SATUNYA tempat menyambung semua service (wiring)
│   ├── services/             # satu folder per domain bisnis
│   ├── clients/              # panggilan ke layanan pihak ketiga, satu file per layanan
│   └── pkg/                  # pustaka bersama yang tidak tahu soal domain
├── skills/                   # folder skill yang dibaca middleware (PATH_FILE_UPLOAD)
├── docker/redis-dev/         # Redis untuk pengembangan, dijalankan di VPS
├── docs/                     # dokumentasi ini
├── Dockerfile                # build image (binary Go statis di alpine)
├── docker-compose.yml        # menjalankan middleware di VPS
├── mcp.json                  # daftar server MCP (tanpa rahasia)
├── .dockerignore
├── logs/                     # dibuat otomatis
├── .env / .env.example
└── go.mod / go.sum           # daftar library; KEDUANYA wajib ada
```

## Isi `internal/services/`

| Folder | Tugas | Tabel |
|---|---|---|
| `skillstore/` | Upload zip, daftar skill, menyiapkan teks skill (bersihkan, budget, `SHORT.md`), preview | - (file di `skills/`) |
| `skillindex/` | Index skill di memori, pemilihan skill (BM25/embedding), keputusan skill aktif, watcher folder | - |
| `skillcache/` | Cache Redis: skill aktif per percakapan dan hasil seleksi | - (Redis) |
| `skillfallback/` | Pemilih cadangan: bertanya ke model saat skor ambigu | - |
| `proxy/` | Proxy `/v1/*`, penyisipan skill/context pack/catatan, pencatatan pemakaian | - |
| `usagelog/` | Log pemakaian token dan ringkasannya | `t_usage_logs` |
| `eval/` | Mengukur kualitas skill (full vs short vs tanpa skill) | `t_eval_runs` |
| `contextpack/` | Context pack: ringkasan tetap yang selalu disisipkan | `m_context_packs` |
| `memorynote/` | Catatan memori: ekstraksi, konfirmasi, pencarian teks | `t_memory_notes` |
| `mcp/` | Koneksi ke server MCP: daftar server dari `mcp.json`, login OAuth, index dan pemilihan tool, pemanggilan tool | `m_mcp_credentials` |

## Aturan file di dalam satu service

Setiap file di `internal/services/<domain>/` harus punya peran yang jelas. Hanya nama-nama berikut yang dipakai:

| File | Peran |
|---|---|
| `controller.go` | Menerima request HTTP, validasi input, memanggil service, mengirim respons |
| `service.go` | Logika bisnis |
| `repository.go` | Akses data (database, file, atau Redis) |
| `route.go` | Mendaftarkan endpoint (`RegisterRoutes`) |
| `dto.go` | Bentuk data input/output (bukan `types.go`) |
| `model.go` | Struct tabel database dan konstanta domain |

File tanpa peran yang jelas (misalnya `snapshot.go`, `index.go`, `selector.go`) **tidak boleh** ditaruh di folder service. Kode seperti itu masuk ke salah satu file di atas, atau dipindah ke `internal/pkg/` bila sifatnya umum.

## Folder `clients/`

Semua panggilan ke layanan luar ada di sini, satu file per layanan. Kalau layanannya berubah, cukup satu file ini yang disentuh.

| File | Layanan |
|---|---|
| `9router.go` | Reverse proxy ke 9Router (streaming, timeout, format error) dan `Complete` untuk panggilan middleware sendiri |
| `redis.go` | Koneksi Redis dengan timeout pendek; error dianggap cache miss |
| `embedding.go` | Klien embedding umum (`POST /embeddings`), untuk 9Router, Ollama, dan sejenisnya |
| `mcp.go` | Klien server MCP (Streamable HTTP, memakai SDK resmi `modelcontextprotocol/go-sdk`): daftar tool dan pemanggilan tool |
| `mcpauth.go` | Discovery OAuth server MCP dan pendaftaran klien otomatis (DCR) |

## Folder `pkg/`

| Paket | Isi |
|---|---|
| `response/` | Bentuk respons seragam `{status, message, data, errors}` |
| `apperror/` | Error aplikasi dan `Handle` (memetakan error ke status HTTP, termasuk 409 untuk data ganda) |
| `uuidparam/` | Middleware yang menolak `:id` bukan UUID dengan 400 |
| `frontmatter/` | Membaca frontmatter `SKILL.md` (`name`, `description`, `keywords`) |
| `bm25/` | Tokenizer Indonesia/Inggris (dengan kata umum dan pemotong akhiran ringan) dan skor BM25 |
| `skillclean/` | Pembersih isi skill dari bagian khusus Claude |
| `modelbudget/` | Budget token per pola nama model |
| `tokens/` | Perkiraan token (karakter / 4) dan pemotongan teks |
| `openai/` | Membaca `usage` dari respons JSON dan SSE |
| `vector/` | Kemiripan kosinus untuk embedding |
| `secretbox/` | Enkripsi AES-256-GCM untuk token yang disimpan di database |

## Pola ketergantungan

Service tidak memanggil service lain secara langsung. Ia mendefinisikan **interface kecil** di sisi pemakai, lalu `router.go` menyambungkan implementasinya. Contoh di `proxy/service.go`:

```go
// Memory dipenuhi oleh memorynote.Service
type Memory interface {
    Relevant(ctx context.Context, query string) []memorynote.Hit
    Block(hits []memorynote.Hit) string
    Observe(userText string, recent []memorynote.Turn)
}
```

Keuntungannya, `proxy` tidak perlu tahu bagaimana catatan disimpan. Bila nanti diganti, cukup implementasinya yang berubah.

## Aturan penamaan tabel

| Awalan | Jenis | Contoh |
|---|---|---|
| `t_` | Tabel transaksi (bertambah terus) | `t_usage_logs`, `t_eval_runs`, `t_memory_notes` |
| `m_` | Tabel master (data acuan yang diatur admin) | `m_context_packs`, `m_mcp_credentials` |

Semua tabel dibuat atau diperbarui otomatis saat server start (`database.Migrate` di `router.go`). Kolom baru di tabel yang sudah berisi data harus diberi `not null` dan `default`, supaya baris lama tidak terisi NULL yang gagal dibaca ke field `int`/`string`.

## Gaya penulisan

- Komentar kode berbahasa Indonesia, huruf kecil, menjelaskan **alasan**, bukan mengulang kode.
- Tidak ada file test. Verifikasi memakai `go build ./...`, `go vet ./...`, atau program sementara di `cmd/tmpcheck` yang dihapus setelah dipakai. Bila nanti test dibutuhkan, tempatnya di folder `test/` di root.

## Menambah service baru (urutan kerja)

1. Buat `internal/services/<nama>/` dengan file-file sesuai tabel peran di atas.
2. Bila butuh layanan luar, tambahkan file di `internal/clients/`.
3. Bila punya tabel, tambahkan struct-nya ke `database.Migrate(...)` di `router.go`.
4. Sambungkan service dan daftarkan route di `router.go`.
5. Variabel konfigurasi baru: tambahkan ke `internal/config/config.go`, lalu ke `.env` **dan** `.env.example` dengan komentar singkat berbahasa Indonesia. Nilai asli hanya di `.env`.
6. Jalankan `go build ./... && go vet ./...`.
7. Perbarui dokumen di `docs/`.
