# 5. Memori

Ada dua jenis memori. Keduanya **per user** (header `X-User-Id`): context pack dan catatan milik user A tidak pernah muncul di chat user B. Keduanya disimpan di PostgreSQL dengan kolom `user_id`. Ini berbeda dengan Redis, yang hanya menyimpan cache sementara.

| | Context pack | Catatan memori |
|---|---|---|
| Tabel | `m_context_packs` (master) | `t_memory_notes` (transaksi) |
| Isi | Fakta tetap: profil bisnis, istilah, aturan | Pengalaman: "dicoba X, hasilnya Y, pelajarannya Z" |
| Siapa yang menulis | Admin lewat API | Otomatis dari percakapan, atau manual lewat API |
| Kapan disisipkan | **Selalu**, di setiap request | Hanya yang **cocok** dengan pesan user, maks 2 |
| Kedaluwarsa | Tidak | 90 hari sejak dikonfirmasi |

## Context pack

Pakai untuk informasi yang selalu benar dan selalu berguna.

```bash
curl -X POST http://localhost:4000/api/context-packs -H "Content-Type: application/json" \
  -d '{"name":"bisnis","content":"Kedai kopi di Bandung, jual kopi susu literan via Instagram dan GoFood. Target: mahasiswa."}'
```

- Semua context pack yang `active: true` digabung, diurutkan menurut nama, lalu dibatasi `CONTEXT_PACK_MAX_TOKENS` (800).
- Untuk menyimpan tanpa menyisipkan: `PUT /api/context-packs/:id` dengan `{"active": false}`.
- Teksnya di-cache di memori selama 30 detik. Perubahan lewat API langsung berlaku.
- Jaga isinya tetap singkat, karena dikirim di **setiap** request dan menambah token.

## Catatan memori

### Bentuk catatan

| Field | Contoh |
|---|---|
| `topic` | promo diskon kopi susu |
| `tried` | promo diskon 50% kopi susu literan selama seminggu |
| `result` | penjualan naik 2x tapi margin habis, rugi Rp1,2 juta |
| `result_source` | `data` (ada angka terukur) atau `user` (klaim/kesan user) |
| `lesson` | diskon besar menaikkan volume tapi menggerus margin; coba bundling |

### Siklus hidup

```
                     pesan user berisi kabar hasil
                                 │
                                 ▼
                       ┌───────────────────┐
     ditulis manual ──▶│ active            │◀── confirm ──┐
     (POST /api/...)   └───────────────────┘              │
                                 ▲                ┌───────┴──────┐
                                 │                │   proposed   │◀── ekstraksi otomatis
                          ┌──────┴──────┐         └───────┬──────┘
          PUT (koreksi) ─▶│  corrected  │                 │ reject
                          └─────────────┘                 ▼
                                                   ┌────────────┐
                    DELETE ──────────────────────▶ │ rejected / │  (tidak diusulkan lagi)
                                                   │ deleted    │
                                                   └────────────┘
```

| Status | Disisipkan ke prompt? | Cara masuk ke status ini |
|---|---|---|
| `proposed` | tidak | Hasil ekstraksi otomatis, menunggu diperiksa |
| `active` | ya | `POST /:id/confirm`, atau catatan manual |
| `corrected` | ya | `PUT /:id` (isi diperbaiki manusia) |
| `rejected` | tidak | `POST /:id/reject` |
| `deleted` | tidak | `DELETE /:id` |

Catatan yang ditolak atau dihapus **tetap disimpan barisnya**. Tujuannya supaya ekstraksi tidak mengusulkan isi yang sama lagi.

### 1. Ekstraksi otomatis

Setelah jawaban chat selesai dikirim ke client:

1. Pesan user terakhir diperiksa. Ekstraksi hanya jalan bila ada **kata sinyal hasil**, misalnya hasil, berhasil, gagal, omzet, laku, laris, naik, turun, untung, rugi, terjual, penjualan, konversi, anjlok, boncos, revenue, sales, worked, failed, ROI, ROAS, CTR. Pesan yang lebih pendek dari 20 karakter dilewati.
2. Model chat milik user (field `model` request itu, dengan key `X-Router-Key`-nya) membaca pesan itu ditambah sampai 6 pesan sebelumnya sebagai konteks.
3. Model hanya boleh mencatat hal yang **sudah dicoba dan hasilnya sudah terjadi**, bukan rencana, pertanyaan, atau saran asisten. Maksimal 3 catatan per pesan.
4. Catatan disimpan berstatus `proposed`.

Pengaman:
- Pesan yang sama tidak diekstrak dua kali, misalnya saat request diulang client.
- Catatan dengan isi sama (topik + yang dicoba + hasil) tidak tersimpan dua kali.
- Ekstraksi berjalan di latar belakang dan tidak pernah memperlambat jawaban. Jumlah yang boleh berjalan bersamaan dibatasi `MEMORY_EXTRACT_CONCURRENCY`. Bila penuh, ekstraksi dilewati.
- Biaya token ekstraksi dicatat di log aplikasi dengan pesan `ekstraksi memori selesai`.

Contoh nyata dari pesan "Udah aku jalanin minggu lalu, iklan IG Rp500 ribu targeting mahasiswa Bandung, hasilnya cuma terjual 3 botol, rugi banget":

```json
{
  "topic": "iklan IG kopi literan",
  "tried": "Pasang iklan Instagram budget Rp500 ribu dengan targeting mahasiswa di Bandung untuk kopi literan",
  "result": "Hanya terjual 3 botol, dianggap rugi",
  "result_source": "data",
  "lesson": "Iklan IG dengan budget Rp500 ribu dan targeting mahasiswa belum tentu efektif untuk kopi literan; perlu evaluasi konten, penawaran, atau segmen target.",
  "status": "proposed"
}
```

### 2. Memeriksa catatan usulan

```bash
# daftar yang menunggu
curl "http://localhost:4000/api/memory-notes?status=proposed"

# setujui
curl -X POST http://localhost:4000/api/memory-notes/<id>/confirm

# perbaiki isinya (otomatis jadi corrected dan langsung dipakai)
curl -X PUT http://localhost:4000/api/memory-notes/<id> -H "Content-Type: application/json" \
  -d '{"lesson":"iklan IG ke mahasiswa kurang efektif untuk kopi literan; uji kreatif kecil dulu"}'

# tolak
curl -X POST http://localhost:4000/api/memory-notes/<id>/reject
```

### 3. Penyisipan ke prompt

Untuk setiap pesan user:
1. Catatan `active`/`corrected` yang belum kedaluwarsa dicari dengan pencarian teks PostgreSQL (kolom `search_vector`, index GIN).
2. Yang dipakai hanya catatan yang memuat minimal `MEMORY_MIN_MATCH` (2) kata dari pesan.
3. Maksimal `MEMORY_MAX_NOTES` (2) catatan, masing-masing maks `MEMORY_NOTE_MAX_TOKENS` (150).

Model diberi instruksi bahwa catatan ini adalah **pengingat yang perlu dicek ulang, bukan larangan**, karena kondisi bisa berubah. Bila rencana user mengulang pola yang dulu gagal, model menyebutkannya lebih dulu lalu menawarkan alternatif.

Hasil uji nyata: dengan catatan "diskon 50% → rugi Rp1,2 juta", pertanyaan "aku mau bikin promo diskon 50% lagi" dijawab:

> Sebentar, aku ingat kamu pernah coba promo ini sebelumnya — hasilnya penjualan memang naik 2x, tapi margin habis dan justru rugi Rp1,2 juta. Daripada ulangi pola yang sama, coba alternatif seperti bundling atau diskon lebih kecil 15-20% ...

Untuk melihat catatan apa yang akan ikut tanpa mengirim chat:

```bash
curl "http://localhost:4000/api/memory-notes/match?q=mau+promo+diskon+50%25+lagi"
```

### Catatan teknis

- PostgreSQL tidak punya kamus bahasa Indonesia, jadi pencarian memakai prefix: `diskon:*` juga cocok dengan "diskonnya". Hasilnya disaring lagi dengan tokenizer yang sama dengan pemilihan skill.
- Kolom `search_vector` adalah generated column yang dihitung PostgreSQL sendiri. Kolom ini sengaja tidak ada di struct GORM dan dibuat oleh `Repository.Migrate()`.
- Pencarian dibatasi `MEMORY_LOOKUP_TIMEOUT` (300 ms). Bila lewat, request jalan tanpa catatan.
- Kata umum seperti "kopi" pada bisnis kopi bisa membuat catatan yang hanya mirip sebagian ikut tersisip. Bila terasa terlalu longgar, naikkan `MEMORY_MIN_MATCH` ke 3.

### Mematikan sebagian

| Ingin | Atur |
|---|---|
| Matikan ekstraksi otomatis (catatan manual tetap jalan) | `MEMORY_EXTRACT_ENABLED=false` |
| Matikan penyisipan catatan | `MEMORY_MAX_NOTES=0` |
| Catatan tidak pernah kedaluwarsa | `MEMORY_NOTE_TTL=0` |
| Matikan context pack | Nonaktifkan semua context pack (`active: false`) |
