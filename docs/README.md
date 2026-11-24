# Dokumentasi middleware-skill-ai

Middleware ini berdiri di antara aplikasi (client) dan **9Router**. Setiap kali client mengirim chat, middleware:

1. memilih **skill** yang paling cocok dengan pesan user (tanpa memanggil AI);
2. menyisipkan isi skill, **context pack**, dan **catatan memori** yang relevan ke awal system prompt;
3. bila pesannya menyinggung aplikasi lain (misalnya Canva), ikut mengirim **tool MCP** yang cocok dan menjalankan tool yang diminta model;
4. meneruskan request ke 9Router dan mengalirkan jawabannya balik ke client;
5. mencatat pemakaian token, lalu mengekstrak pelajaran dari pesan user di latar belakang.

Bagi client, middleware ini terlihat sama persis seperti 9Router (format OpenAI). Cukup ganti alamatnya.

## Daftar dokumen

| No | Dokumen | Isi |
|---|---|---|
| 1 | [Cara kerja](01-cara-kerja.md) | Alur satu request dari awal sampai akhir, dengan gambar |
| 2 | [Struktur kode](02-struktur-kode.md) | Folder, peran tiap file, aturan penamaan, cara menambah fitur |
| 3 | [Skill](03-skill.md) | Apa itu skill, format folder, upload zip, SHORT.md, skill yang tidak cocok |
| 4 | [Pemilihan skill](04-pemilihan-skill.md) | BM25, threshold, sesi percakapan, cache, pemilih cadangan, embedding |
| 5 | [Memori](05-memori.md) | Context pack dan catatan memori (ekstraksi, konfirmasi, penyisipan) |
| 6 | [Log dan eval](06-log-dan-eval.md) | Log pemakaian token dan cara mengukur apakah skill berguna |
| 7 | [API](07-api.md) | Semua endpoint beserta contoh `curl` |
| 8 | [Konfigurasi](08-konfigurasi.md) | Semua variabel `.env` dan nilai bawaannya |
| 9 | [Menjalankan, deploy, dan masalah umum](09-menjalankan-dan-deploy.md) | Menjalankan lokal, Redis DEV, deploy dengan Docker Compose, dan solusi error yang sering muncul |
| 10 | [MCP](10-mcp.md) | Koneksi ke Canva, Figma, dan server MCP lain: login OAuth, pemilihan tool, putaran panggil-tool |
| 11 | [Gambar lewat chat](11-gambar.md) | Tool bawaan `generate_image`: chat "buatkan gambar ..." menghasilkan gambar yang tampil di jawaban |
| 12 | [Akun, login, dan role](12-akun-dan-login.md) | Register/login/refresh token, role admin dan user, siapa melihat data siapa |
| 13 | [Pasang MCP dari GitHub](13-pasang-mcp-dari-github.md) | Chat "pasangkan repo ini jadi MCP": analisis, persetujuan admin, data rahasia lewat form, container Docker terpisah |

## Ringkasan cepat

- **Bahasa dan pustaka:** Go, Gin (HTTP), GORM + PostgreSQL, Redis, zap (log).
- **Wajib saat start:** PostgreSQL, Redis, dan alamat serta key 9Router. Bila salah satu gagal, server berhenti dengan pesan error.
- **Autentikasi:** aplikasi pihak ketiga memakai `X-API-Key` + `X-User-Id`; user yang register di middleware memakai `Authorization: Bearer <access_token>`. Admin melihat data semua user, user hanya miliknya; skill global. Chat memakai 9Router milik user (`X-Router-URL`, `X-Router-Key`, `model`).
- **Folder skill:** `skills/` (diatur `PATH_FILE_UPLOAD`). Hanya folder ini yang dibaca middleware.
- **Asal desain:** dokumen *Desain Middleware Skill untuk 9Router*, Tahap 1 sampai 4. Semua tahap sudah dibangun.

| Tahap | Isi | Status |
|---|---|---|
| 1 | Proxy streaming ke 9Router, upload dan daftar skill, pemilihan skill BM25 | selesai |
| 2 | Budget token per model, `SHORT.md`, cache Redis, log pemakaian, pembersihan isi skill, reload otomatis | selesai |
| 3 | Embedding, pemilih cadangan, eval skill | selesai (embedding menunggu provider di 9Router) |
| 4 | Context pack, catatan memori, ekstraksi di latar belakang | selesai |
| + | MCP: middleware menjadi klien MCP (Canva, server MCP lain) karena 9Router tidak bisa | selesai; Figma resmi menolak pendaftaran klien (lihat dokumen 10) |
