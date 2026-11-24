# 4. Pemilihan skill

Skill dipilih **tanpa memanggil AI** di kebanyakan kasus, supaya cepat dan murah. Hanya satu skill yang dipakai per request.

## BM25 (cara utama)

BM25 adalah rumus pencarian teks klasik: semakin banyak kata pesan yang muncul di data skill, dan semakin jarang kata itu di skill lain, semakin tinggi skornya.

Yang dicocokkan dari setiap skill:

| Bagian | Bobot |
|---|---|
| `name` | ×3 |
| `keywords` | ×2 |
| `description` | ×1 |

- Tokenizer membuang kata umum Indonesia/Inggris ("yang", "dan", "the", ...) dan memotong akhiran ringan (-nya, -lah, -kah, -pun, "s" jamak).
- Skill hanya dipilih bila skor teratas ≥ `SKILL_MIN_SCORE` (bawaan 2,0).
- Sapaan seperti "halo" atau "terima kasih" tidak menghasilkan skor, jadi tidak mendapat skill.

### Menyetel threshold

```bash
curl "http://localhost:4000/api/skills/match?q=berapa+harga+yang+pas+untuk+produk+saya"
```

Hasilnya menunjukkan skor semua kandidat dan skill mana yang `selected`. Naikkan `SKILL_MIN_SCORE` bila skill sering salah terpilih. Turunkan bila skill jarang terpilih padahal relevan.

## Skill aktif per percakapan (sesi)

Model tidak mengingat apa pun antar request, jadi isi skill harus dikirim ulang setiap kali. Masalahnya, pesan lanjutan seperti "oke, lanjut" tidak mengandung kata kunci apa pun.

Solusinya, skill yang terakhir dipakai disimpan di Redis per percakapan (`SKILL_SESSION_TTL`, 45 menit, diperpanjang setiap dipakai). Aturannya:
- Pesan tanpa skill yang cocok → tetap memakai skill aktif.
- Ada skill lain yang cocok → skill aktif hanya diganti bila skill baru **jauh lebih cocok**: skornya minimal `SKILL_SWITCH_RATIO` (1,5) kali skor skill aktif untuk pesan yang sama.

**Mengenali percakapan.** Format OpenAI tidak punya ID percakapan, jadi dipakai:
1. header `X-Conversation-Id` bila client mengirimnya (disarankan), atau
2. pesan user pertama, yang selalu ikut terkirim di setiap request.

`user_id` (header `X-User-Id`) ikut di-hash supaya percakapan dua user berbeda tidak tertukar. Yang disimpan di Redis hanya hash, bukan isi pesan.

## Cache hasil seleksi

Pesan yang sama setelah dinormalisasi (huruf kecil, tanpa kata umum, urutan kata diabaikan) memakai hasil sebelumnya selama `SKILL_SELECTION_TTL` (1 jam). Cache otomatis tidak berlaku saat index skill berubah. Keputusan pemilih cadangan ikut di-cache, jadi pesan yang sama tidak memanggil model dua kali. Hasil yang gagal (timeout, error) tidak di-cache.

Semua key Redis berawalan `skillmw:`.

## Pemilih cadangan (memanggil model hanya saat ragu)

Model chat milik user (field `model`, dengan key `X-Router-Key`-nya) dipanggil hanya bila skor **ambigu**. Nyalakan/matikan dengan `SKILL_FALLBACK_ENABLED`:
- dua skill atau lebih bersaing ketat (skor ≥ `SKILL_AMBIGUOUS_RATIO` × skor teratas, bawaan 0,8), atau
- skor teratas sedikit di bawah threshold (antara 50% dan 100% dari `SKILL_MIN_SCORE`).

Model diberi pesan user dan maksimal 3 kandidat, lalu hanya boleh menjawab satu nama kandidat atau `none`. Jawaban lain dianggap gagal, dan keputusan kembali ke skor.

Biaya nyata: sekitar 2–7 detik dan sekitar 2.200 token per panggilan. `SKILL_FALLBACK_TIMEOUT` (5 detik) membatasi waktu tunggunya. Untuk mengecek apakah sebuah pesan ambigu: `GET /api/skills/match?q=...` (lihat field `ambiguous`), tambah `&fallback=true&model=<model>` dan header `X-Router-Key` untuk benar-benar menjalankannya.

Jangan pakai model `kr/*` di 9Router. Saat ini model itu selalu menolak ("This version of Kiro is no longer supported").

## Embedding (siap, belum aktif)

Embedding memilih skill berdasarkan **makna**, bukan kata yang sama persis. Dengan embedding, pesan Indonesia bisa cocok dengan deskripsi berbahasa Inggris tanpa `keywords`.

Status: **mati selama `EMBEDDING_MODEL` kosong**, karena 9Router belum punya provider embedding. Cara mengaktifkan:
- tambahkan provider embedding di dashboard 9Router, lalu isi `EMBEDDING_MODEL`, `EMBEDDING_BASE_URL` (alamat 9Router itu), dan `EMBEDDING_API_KEY`; atau
- pasang Ollama dengan model `bge-m3`, lalu isi `EMBEDDING_BASE_URL=http://localhost:11434/v1` dan `EMBEDDING_MODEL=bge-m3`.

Perilakunya:
- Bila diisi, embedding wajib terjangkau saat start.
- Vektor tiap skill disimpan di `skills/<nama>/.embedding.json` dan hanya dihitung ulang bila deskripsi atau modelnya berubah.
- Saat request, hanya pesan user yang di-embed (`EMBEDDING_TIMEOUT`, 2 detik). Bila gagal, request itu memakai BM25.
- Skornya berskala -1 sampai 1, jadi punya threshold sendiri: `SKILL_MIN_SIMILARITY`, `SKILL_SWITCH_MARGIN`, dan `SKILL_AMBIGUOUS_MARGIN`. Nilai bawaannya baru tebakan awal dan perlu disetel lewat `/api/skills/match` setelah modelnya dipilih.

## Budget token per model

Setelah skill terpilih, panjang teksnya disesuaikan dengan model:

```env
SKILL_BUDGET_DEFAULT=3000
SKILL_BUDGETS=*mini*=800,*haiku*=800,*flash*=800
```

Aturan pertama yang cocok dipakai (`*` = wildcard, huruf besar/kecil tidak dibedakan). Bila skill melebihi budget, dipakai `SHORT.md`. Bila tidak ada, teksnya dipotong. Varian yang dipakai (`full`, `short`, `truncated`) tercatat di log pemakaian.
