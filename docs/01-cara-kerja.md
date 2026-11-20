# 1. Cara kerja

## Gambaran besar

```
┌──────────┐   POST /v1/chat/completions   ┌──────────────────────┐   request + sisipan   ┌──────────┐
│  Client  │ ────────────────────────────▶ │  middleware-skill-ai │ ────────────────────▶ │ 9Router  │
│ (app/SDK │                               │                      │                       │ (model   │
│  OpenAI) │ ◀──────────────────────────── │                      │ ◀──────────────────── │   AI)    │
└──────────┘   jawaban (JSON atau SSE)     └──────────┬───────────┘   jawaban apa adanya  └──────────┘
                                                      │
                     ┌────────────────┬───────────────┼────────────────┐
                     ▼                ▼               ▼                ▼
               folder skills/     Redis          PostgreSQL       9Router lagi
               (isi skill)    (cache sesi &    (log, eval,      (hanya untuk pemilih
                               hasil seleksi)   context pack,     cadangan, eval, dan
                                                catatan memori)   ekstraksi memori)
```

Middleware **tidak menjawab sendiri**. Ia hanya "membekali" request dengan pengetahuan tambahan sebelum meneruskannya ke 9Router. Model di 9Router yang menjawab.

## Analogi

Bayangkan 9Router adalah konsultan yang pintar tapi pelupa. Setiap kali kamu bertanya, middleware:

1. membaca pertanyaanmu, lalu mengambil **buku panduan** yang paling cocok dari rak (skill);
2. menyelipkan **profil bisnismu** (context pack);
3. menyelipkan **catatan pengalaman lama** yang mirip dengan pertanyaanmu (catatan memori), misalnya "dulu diskon 50% bikin rugi";
4. menyerahkan semuanya ke konsultan, lalu membawa jawabannya kembali ke kamu;
5. setelah selesai, mencatat kalau kamu baru saja melaporkan hasil sesuatu, supaya bisa jadi catatan baru.

## Alur satu request chat, langkah demi langkah

Hanya `POST /v1/chat/completions` yang diubah. Path `/v1/*` lain (misalnya `/v1/models`) diteruskan polos.

| Langkah | Yang terjadi | Kode |
|---|---|---|
| 1 | Body dibaca (maks 20 MB). Bila field `stream` tidak ada, diisi `false` karena 9Router menjawab SSE bila field itu kosong. | `services/proxy/controller.go` |
| 2 | Pesan user terakhir diambil sebagai bahan pencarian. | `services/proxy/service.go` (`Prepare`) |
| 3 | Skill dipilih: cek cache hasil seleksi di Redis, bila tidak ada hitung BM25 (atau embedding bila aktif). Bila skornya ambigu, pemilih cadangan bertanya ke model. | `services/skillindex`, `services/skillcache`, `services/skillfallback` |
| 4 | Hasil ditimbang dengan skill yang sedang aktif di percakapan ini (cache sesi), supaya pesan lanjutan seperti "oke lanjut" tetap memakai skill yang sama. | `skillindex.Choose` |
| 5 | Isi skill disiapkan sesuai budget token model: dibersihkan, diganti `SHORT.md`, atau dipotong. | `services/skillstore` (`Render`) |
| 6 | Context pack aktif diambil dan catatan memori yang cocok dicari. | `services/contextpack`, `services/memorynote` |
| 7 | Semuanya digabung dengan urutan **skill → context pack → catatan**, lalu ditaruh di awal pesan system. Bila belum ada pesan system, dibuat satu. | `injectSystem` |
| 7b | Tool MCP yang cocok dengan pesan (bila ada) ditambahkan ke field `tools`. | `services/mcp` (`Select`) |
| 8 | Tanpa tool: request diteruskan ke 9Router dan jawabannya (termasuk SSE) langsung dialirkan ke client. Dengan tool: middleware menjalankan putaran panggil-tool sampai ada jawaban akhir (lihat [MCP](10-mcp.md)). | `clients/9router.go`, `proxy` (`RunWithTools`) |
| 9 | Pemakaian token dicatat ke `t_usage_logs` di latar belakang. | `services/usagelog` |
| 10 | Bila pesan user berisi kabar hasil (berhasil, gagal, omzet, rugi, ...), catatan usulan diekstrak di latar belakang. | `services/memorynote` (`Observe`) |

## Contoh sisipan yang dikirim ke model

```text
<skill name="founder-pricing">
...isi skill...
</skill>

<context_pack>
## bisnis
Kedai kopi di Bandung, jual kopi susu literan via Instagram dan GoFood.
</context_pack>

<memory_notes>
Catatan pengalaman sebelumnya. Ini pengingat yang perlu dicek ulang, bukan larangan: ...
- [promo diskon kopi susu, dicatat 2026-11-20] Dicoba: promo diskon 50% ... | Hasil: penjualan naik 2x tapi rugi Rp1,2 juta (sumber: data) | Pelajaran: ...
</memory_notes>

...system prompt asli dari client (bila ada)...
```

## Prinsip "gagal terbuka"

Skill, context pack, dan catatan hanyalah **bonus**. Bila salah satu bagian gagal, request tetap diteruskan:

| Yang gagal | Akibatnya |
|---|---|
| Body bukan JSON atau file skill hilang | Request diteruskan tanpa sisipan, masalah dicatat di log |
| Redis mati saat server sudah berjalan | Cache dilewati (status `off`), pulih sendiri saat Redis hidup lagi |
| Embedding gagal | Request itu memakai BM25 |
| Pemilih cadangan timeout | Keputusan diambil dari skor saja |
| Pencarian catatan memori lambat atau gagal | Request jalan tanpa catatan |
| Database gagal saat membaca context pack | Teks context pack terakhir tetap dipakai |

Yang **tidak** gagal terbuka hanya kondisi saat start: PostgreSQL, Redis, dan 9Router wajib terjangkau. Kalau embedding diaktifkan, embedding juga wajib.

## Siapa memanggil 9Router

| Pemanggil | Key yang dipakai | Kapan |
|---|---|---|
| Proxy (request client) | Key milik **client** (header `Authorization` diteruskan apa adanya) | Setiap chat |
| Pemilih cadangan | `AI_ROUTER_API_KEY` milik middleware | Hanya saat skor ambigu |
| Eval | `AI_ROUTER_API_KEY` | Saat eval dijalankan lewat API |
| Ekstraksi memori | `AI_ROUTER_API_KEY` | Hanya bila pesan berisi kabar hasil |
| Putaran tool MCP | Key milik **client** | Hanya bila pesan membawa tool MCP; satu request bisa memanggil model beberapa kali |

Setiap panggilan ke 9Router menambah sekitar 2.100–2.200 token prompt dari sisi 9Router/provider, meskipun prompt dari middleware jauh lebih kecil. Perhitungkan ini saat memperkirakan biaya.
