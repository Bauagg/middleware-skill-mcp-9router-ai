# 3. Skill

## Apa itu skill

Skill adalah **teks panduan** untuk model AI tentang satu jenis tugas, misalnya cara menentukan harga atau cara menulis caption. Konsepnya sama dengan skill di Claude: satu folder berisi `SKILL.md`.

Bedanya, di middleware ini **hanya teksnya yang dipakai**. Model di belakang 9Router tidak bisa menjalankan script, membuka file, atau memakai tool. Jadi skill harus bisa dipahami dan dijalankan hanya dengan membaca teksnya.

## Letak skill

Middleware hanya membaca folder yang diatur `PATH_FILE_UPLOAD` di `.env`. Nilai bawaannya **`skills/`**. Folder ini dibuat otomatis bila belum ada, dan isinya tidak di-commit (gitignore).

Folder skill di luar `skills/` tidak akan terbaca.

```
skills/
├── example/
│   └── SKILL.md
└── founder-pricing/
    ├── SKILL.md          # wajib: isi lengkap
    ├── SHORT.md          # opsional: ringkasan 200-400 token
    ├── EVAL.yaml         # opsional: soal uji untuk eval
    └── .embedding.json   # dibuat otomatis bila embedding aktif
```

Folder berawalan titik (misalnya `.upload-*` dan `.old-*`) adalah folder sementara milik proses upload dan selalu diabaikan.

## Format `SKILL.md`

```markdown
---
name: founder-pricing
description: Finds the price buyers will actually pay. Use when the user asks about pricing a product.
keywords: harga, menentukan harga, tarif, berapa harga
---

# Isi skill

Langkah-langkah, aturan, contoh...
```

| Field | Wajib | Aturan |
|---|---|---|
| `name` | ya | huruf kecil/angka, dipisah `-` atau `_`, maks 64. Menjadi nama folder. |
| `description` | ya | maks 1024. Jelaskan **kapan** skill dipakai, karena ini yang dicocokkan dengan pesan user. |
| `keywords` | tidak | padanan kata, dipisah koma atau daftar YAML. Penting bila deskripsi berbahasa Inggris tapi user bertanya dalam bahasa Indonesia. |

Contoh nyata: tanpa `keywords`, pesan "berapa harga yang pas untuk produk saya" tidak mendapat skill apa pun. Setelah `keywords: harga, menentukan harga` ditambahkan, `founder-pricing` terpilih dengan skor 3,76.

## `SHORT.md`

Ringkasan skill sekitar 200–400 token. Dipakai otomatis bila isi lengkap melebihi budget token model (misalnya model kecil seperti `*mini*`, `*flash*`, `*haiku*` dengan budget 800). Bila `SHORT.md` tidak ada atau masih terlalu panjang, isi skill dipotong di batas paragraf.

`SHORT.md` juga bisa lebih baik daripada isi lengkap. Lihat contoh `founder-pricing` di bawah.

## `EVAL.yaml`

Soal uji untuk mengukur apakah skill berguna. Penjelasan lengkap di [Log dan eval](06-log-dan-eval.md).

```yaml
cases:
  - question: Berapa harga yang pas untuk kopi susu 250ml di dekat kampus?
    criteria: Menyebut rentang harga, membandingkan dengan pesaing, dan memberi satu harga rekomendasi.
```

## Upload skill

```bash
curl -X POST http://localhost:4000/api/skills/upload -F "file=@founder-pricing.zip" -F "overwrite=false"
```

- Zip berisi `SKILL.md` di root (satu skill), atau `<folder>/SKILL.md` untuk tiap skill (banyak skill sekaligus).
- Hanya file teks yang dipasang: `.md .txt .json .yaml .yml .csv`. Script (`.py`, `.sh`, ...) dibuang dan dilaporkan di `skipped_files`.
- Batas: zip maks 10 MB, maks 500 file, 2 MB per file, 20 MB total setelah diekstrak.
- Aman dari zip slip, symlink, dan zip bomb. Pemasangannya atomik: diekstrak ke folder sementara dulu, baru dipindah. Bila gagal, skill lama dipulihkan.
- `overwrite=true` untuk mengganti skill yang sudah ada.

Skill juga boleh disalin langsung ke `skills/` tanpa upload. Watcher akan membacanya dalam beberapa detik.

## Reload otomatis

Index skill (nama, deskripsi, keywords) disimpan di memori. Index dibangun ulang:
- saat server start;
- setelah upload berhasil;
- oleh **watcher** yang memeriksa folder tiap `SKILL_WATCH_INTERVAL` (5 detik). Perubahan baru dimuat setelah stabil dua kali pemeriksaan berturut-turut, supaya file yang sedang ditulis tidak terbaca setengah.

Isi skill tidak disimpan di index, tetapi dibaca dari disk saat skill terpilih. Folder dengan `SKILL.md` rusak dilewati dan dicatat di log tanpa mengganggu skill lain.

## Pembersihan isi

Sebelum disisipkan, isi skill dibersihkan dari bagian yang hanya berguna di Claude:
- frontmatter dan komentar HTML;
- baris yang menyebut `/mnt/skills`, `${CLAUDE_...}`, atau tool Claude (`AskUserQuestion`, `WebSearch`, `mcp__...`, dan sejenisnya);
- blok kode yang menjalankan program (`python3`, `node`, `bash`, `npm`, ...);
- judul yang jadi kosong.

Aturan baru cukup ditambah satu baris di `internal/pkg/skillclean/clean.go`. Untuk melihat teks akhirnya: `GET /api/skills/preview/<nama>?model=<model>`.

Pembersihan hanya **menghapus**. Kalimat seperti "jalankan skrip di bawah" tetap tertinggal, jadi skill yang intinya bergantung pada script tetap tidak berfungsi penuh.

## Skill yang tidak cocok untuk middleware: contoh `founder-pricing`

Repo [founder-skill](https://github.com/Jakeschincariol/founder-skill) berisi 11 skill yang dirancang **berantai**:

```
founder-competitors ──▶ harga pesaing ─┐
founder-consumer ─────▶ panel 100 pembeli simulasi ──▶ founder-pricing ──▶ founder-offer ──▶ ...
                        (menjalankan panel.py)          (menjalankan van_westendorp.py)
```

`founder-pricing` bertugas menentukan harga jual. Ia membaca jawaban harga dari panel pembeli, lalu menjalankan script `van_westendorp.py` untuk menghitung rentang harga yang bisa diterima.

Di middleware, panel dan script itu tidak ada. Akibatnya, dengan isi lengkap, model malah menolak menjawab dan meminta data panel. Hasil eval:

| Varian | Skor (0–10) |
|---|---|
| Isi lengkap (`SKILL.md`) | 2,5 |
| Ringkasan (`SHORT.md`) | 9 |
| Tanpa skill | 7,5 |

Pelajarannya: skill yang bergantung pada skill lain, data file, atau script harus **ditulis ulang supaya berdiri sendiri** sebelum dipasang. Contohnya, skill menanyakan data yang dibutuhkan langsung ke user, lalu menghitungnya sendiri dalam teks. Pilihan lainnya, pakai `SHORT.md` sebagai isi utama.

## Tips menulis skill yang baik untuk middleware

- Tulis `description` dari sudut pandang **kapan dipakai**: "Dipakai saat user bertanya soal ...".
- Tambahkan `keywords` dalam bahasa yang dipakai user.
- Jangan bergantung pada script, file lain, atau skill lain.
- Bila butuh data, minta model menanyakannya ke user.
- Sediakan `SHORT.md` untuk model kecil.
- Sediakan `EVAL.yaml`, lalu jalankan eval setiap kali skill diubah.
