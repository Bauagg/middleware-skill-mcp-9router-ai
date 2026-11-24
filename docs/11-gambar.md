# 11. Membuat gambar lewat chat

## Gambaran

User cukup menulis di chat, misalnya "buatkan gambar logo kedai kopi Kopi Senja, minimalis warna coklat". Jawabannya berisi gambar yang langsung tampil di aplikasi chat:

```markdown
Berikut logonya!

![Logo minimalis kedai kopi Kopi Senja](https://<BASE_URL>/images/20261123-3d6d7e80b856d520.png)
```

Model chat di 9Router tidak bisa membuat gambar di dalam chat. Karena itu middleware memberi model sebuah **tool bawaan `generate_image`** yang berjalan lewat putaran tool yang sama dengan [MCP](10-mcp.md):

```
User: "buatkan gambar logo ..."
  │
  ▼
middleware: pesan menyinggung gambar → tawarkan tool generate_image
  │
  ▼
model: panggil generate_image(prompt detail dalam bahasa Inggris, alt)
  │
  ▼
middleware: POST /images/generations (key user, model gambar dari akun user, lewat 9Router)
            → simpan file di IMAGE_PATH → link BASE_URL/images/<nama>
  │
  ▼
model: jawaban + ![alt](link)  →  gambar tampil di chat
```

## Mengaktifkan

Model gambar 9Router **tidak muncul di `GET /v1/models`** dan tidak bisa dimasukkan ke combo. Daftarnya ada di **`GET /v1/models/image`**. Di 9Router saat ini:

| Model | Status |
|---|---|
| `cx/gpt-image-2.5`, `cx/gpt-image-2`, `cx/gpt-image-1.5`, `cx/gpt-5.6-*-image` | Butuh akun ChatGPT **Plus/Pro** yang terhubung ke 9Router. Akun Free ditolak 403 ("ChatGPT Free plans are not supported"). Paling bagus untuk iklan karena teks di gambar rapi. |
| `ag/gemini-3.1-flash-image` | Gratis lewat Antigravity, tetapi sering 503 "No capacity available" (server Google penuh) |

Claude tidak bisa membuat gambar di paket mana pun.

Model gambar **tidak ditulis di `.env`**. Setiap user bisa punya provider gambar berbeda di 9Router-nya, jadi middleware mengambil daftarnya otomatis dari akun user (`GET /v1/models/image` dengan key `X-Router-Key`) dan mencobanya berurutan sesuai urutan dari 9Router:

- Daftar model di-cache 10 menit per akun (1 menit bila gagal diambil).
- Akun yang belum punya model gambar **tidak ditawari** tool `generate_image`, jadi model chat tidak menjanjikan gambar.

- Bila sebuah model gagal, model berikutnya langsung dicoba.
- Model yang menolak **dilewati sementara untuk akun itu saja**, supaya user tidak menunggu model yang pasti gagal:
  - akun tidak berhak (401/402/403): 30 menit;
  - server penuh atau error (429/5xx/timeout): 2 menit;
  - prompt ditolak (400): tidak dilewati.
- Bila semua model gagal atau sedang dilewati, model chat memberi tahu user bahwa layanan gambar sedang tidak tersedia.
- Model yang berhasil dicatat di kolom `model` pada `t_generated_images`.

Menambah atau mengganti model gambar cukup lewat dashboard 9Router akun user, tanpa restart middleware (paling lama 10 menit sampai terbaca).

## Kapan tool ditawarkan

Tool `generate_image` hanya ikut dikirim ke model bila **dua pesan user terakhir** menyinggung gambar, supaya chat biasa tidak bertambah token. Kata yang dikenali:
- awalan `gambar…` (gambar, gambarkan, gambarnya), `ilustrasi…`, `lukis…`, `illustrat…`;
- image, picture, foto, photo, logo, ikon, icon, wallpaper, avatar, banner, poster, thumbnail, sketsa, sketch, draw, drawing, render, artwork, karikatur, maskot, stiker, sticker, kartun, visual, lukisan.

Model tetap yang memutuskan: tool yang ditawarkan belum tentu dipanggil. Contohnya, "apa format gambar terbaik untuk Instagram?" menawarkan tool, tetapi model cukup menjawab dengan teks.

Bila Canva juga terhubung lewat MCP, pesan seperti "buatkan poster" bisa membawa tool Canva **dan** `generate_image`. Model memilih yang paling sesuai, dan user bisa memperjelas ("pakai Canva" atau "gambar biasa saja").

## Penyimpanan dan penyajian

- Gambar disimpan di `IMAGE_PATH` (bawaan `images/`, di Docker volume `images_data`) dengan nama acak berawalan tanggal, misalnya `20261123-3d6d7e80b856d520.png`.
- Gambar disajikan di `GET /images/<nama>` (di luar `/api`) dengan cache panjang, karena isinya tidak pernah berubah.
- Hanya nama berformat `YYYYMMDD-<16 hex>.(png|jpg|webp|gif)` yang dilayani. Nama lain (termasuk `../.env`) dijawab 404.
- **`BASE_URL` harus bisa dibuka oleh aplikasi chat.** Kalau tidak, link gambar tampil sebagai gambar rusak. Di VPS, artinya alamat publik lewat NGINX, dan `/images/*` perlu dibuka ke publik.
- File gambar tidak dihapus otomatis. Bersihkan folder per tanggal bila perlu, karena nama file diawali tanggal.

## Catatan di database (`t_generated_images`)

Setiap gambar yang berhasil dibuat juga dicatat satu baris:

| Kolom | Isi |
|---|---|
| `url` | link lengkap saat gambar dibuat (`BASE_URL/images/<file>`), sama dengan yang muncul di jawaban chat |
| `file_name` | nama file di `IMAGE_PATH`. Bila `BASE_URL` berubah, gambar tetap terbuka di alamat baru dengan nama yang sama. |
| `prompt`, `revised_prompt` | prompt yang ditulis model (bahasa Inggris) dan prompt yang dipakai provider (bila dilaporkan) |
| `alt` | keterangan singkat gambar dalam bahasa user |
| `model`, `mime`, `size_bytes`, `latency_ms` | model gambar, format, ukuran file, dan lama pembuatan |
| `created_at` | waktu dibuat |

Pencatatan ini pelengkap: bila database gagal, gambar tetap dikirim ke user dan kegagalannya dicatat di log aplikasi.

```bash
curl "http://localhost:4000/api/images?q=kopi&page=1&limit=20"   # cari di prompt dan alt
curl "http://localhost:4000/api/images/<id>"
```

## Catatan

- Gambar dibuat memakai **key 9Router milik user** (`X-Router-Key`), jadi biayanya masuk ke akun user.
- Model chat **tidak bisa melihat** gambar hasilnya. Hasil tool memintanya tidak mengarang detail isi gambar, cukup menyebut gambar dibuat dan menawarkan revisi.
- Revisi ("ganti warnanya jadi hijau") membuat gambar **baru** dari prompt yang diperbarui. Gambar lama tidak diedit.
- Pemakaian tercatat di log seperti tool MCP: `mcp_tool_names` berisi `generate_image`, dan `mcp_tool_calls` serta `mcp_steps` ikut terisi.

## Hasil uji

Diuji dengan AI-Crypro asli dan layanan gambar uji yang meniru format OpenAI:

| Pesan | Hasil |
|---|---|
| "Buatkan gambar logo kedai kopi bernama Kopi Senja, gaya minimalis warna coklat." | Model menulis prompt bahasa Inggris yang detail, gambar dibuat dan disimpan, jawaban berisi `![Logo minimalis ...](…/images/….png)`, link terbuka sebagai `image/png` |
| "tolong gambarkan ilustrasi kucing oren sedang minum kopi" (stream) | Sama, dikirim sebagai SSE |
| "halo" | Tidak ada tool, diteruskan biasa |

Uji dengan model gambar sungguhan menunggu provider gambar ditambahkan di 9Router.
