# 6. Log pemakaian dan eval

## Log pemakaian (`t_usage_logs`)

Setiap `POST /v1/chat/completions` dicatat satu baris. Tujuannya menjawab: berapa token tambahan yang dipakai skill dan memori, seberapa sering skill terpilih, dan seberapa lama request.

| Kolom | Isi |
|---|---|
| `user_id` | dari header `X-User-Id` (filter `?user_id=` di API) |
| `model`, `path`, `stream` | dari request |
| `skill_name`, `skill_score` | skill yang disisipkan dan skornya (NULL bila tanpa skill) |
| `extra_tokens_est` | perkiraan token dari skill (karakter / 4) |
| `skill_variant`, `budget_tokens` | `full` / `short` / `truncated`, dan budget model itu |
| `selection_cache`, `session_cache` | `hit` / `miss` / `off`; sesi juga bisa `switch` (skill aktif diganti) |
| `selection_method`, `fallback_tokens` | `bm25` / `embedding` / `fallback`, dan token pemilih cadangan |
| `context_pack_tokens_est` | perkiraan token context pack |
| `memory_notes`, `memory_note_ids`, `memory_tokens_est` | jumlah catatan memori, id-nya (dipisah koma), dan perkiraan tokennya |
| `mcp_tools_offered`, `mcp_tools_tokens_est` | jumlah tool MCP yang dikirim ke model dan perkiraan token definisinya |
| `mcp_tool_calls`, `mcp_tool_names`, `mcp_steps` | berapa kali dan tool apa yang dipanggil, dan berapa kali model dipanggil dalam request itu |
| `prompt_tokens`, `completion_tokens`, `cached_tokens` | dari 9Router; **NULL bila tidak dilaporkan** (beda dengan 0). Pada request dengan tool MCP, ini jumlah semua putaran. |
| `status`, `latency_ms`, `aborted` | status dari 9Router, lama request, dan apakah client memutus koneksi |

Catatan:
- Pencatatan berjalan di latar belakang: ditampung lalu ditulis per 100 baris atau tiap 2 detik. Pencatatan tidak pernah memperlambat request. Catatan yang masih di antrean saat server dimatikan paksa bisa hilang.
- Pada streaming, token hanya dilaporkan bila client mengirim `"stream_options": {"include_usage": true}`. Middleware sengaja tidak menambahkannya supaya respons ke client tidak berubah.
- `memory_note_ids` berguna untuk melacak catatan mana yang memengaruhi sebuah jawaban.

```bash
# daftar log terbaru
curl "http://localhost:4000/api/usage-logs?page=1&limit=20&skill=founder-pricing"

# ringkasan
curl "http://localhost:4000/api/usage-logs/summary?from=2026-11-01&to=2026-12-01"
```

Ringkasan berisi:
- per skill: jumlah request, rata-rata token tambahan, rata-rata token prompt/jawaban/cache, dan latensi;
- jumlah hit/miss cache seleksi dan cache sesi;
- jumlah pemakaian tiap varian skill;
- jumlah request per metode seleksi;
- bagian `memory`: berapa request yang disisipi context pack atau catatan, dan rata-rata tokennya;
- bagian `mcp`: berapa request yang membawa tool dan yang benar-benar memanggil tool, rata-rata token definisi tool, jumlah panggilan, dan putaran.

## Eval skill (`t_eval_runs`)

Eval menjawab dua pertanyaan:
1. Apakah skill benar-benar membuat jawaban lebih baik dibanding tanpa skill?
2. Apakah `SHORT.md` cukup bagus, sehingga bisa dipakai untuk menghemat token?

### Cara menjalankan

**1. Tulis soal** di `skills/<nama>/EVAL.yaml` (disarankan 10–20 soal, maks 30). `criteria` wajib diisi karena itu yang dipakai hakim untuk menilai.

```yaml
cases:
  - question: Berapa harga yang pas untuk kopi susu 250ml di dekat kampus?
    criteria: Menyebut rentang harga, membandingkan dengan pesaing, dan memberi satu harga rekomendasi.
  - question: Apakah saya perlu paket bundling?
    criteria: Menimbang margin per paket dan memberi contoh harga paket.
```

**2. Jalankan** (berjalan di latar belakang dan langsung mengembalikan `run_id`):

```bash
curl -X POST http://localhost:4000/api/evals/runs -H "Content-Type: application/json" \
  -H "X-API-Key: <API_KEYS>" -H "X-User-Id: admin" -H "X-Router-URL: https://<domain-9router>/v1" -H "X-Router-Key: <KEY 9ROUTER>" \
  -d '{"skill":"founder-pricing","model":"AI-Crypro"}'
```

Wajib: `model` (model 9Router yang diuji) dan header `X-Router-Key` (key 9Router, 401 bila tidak ada). Opsi: `variants` (bawaan `["full","short","none"]`), `judge_model` (bawaan sama dengan `model`), dan `limit` (jumlah soal).

**3. Lihat hasil:**

```bash
curl "http://localhost:4000/api/evals/runs/<run_id>"
curl "http://localhost:4000/api/evals/runs/<run_id>?detail=true"   # tiap jawaban, skor, dan alasan hakim
```

Hasilnya berisi skor rata-rata per varian, rata-rata token, jumlah yang gagal, dan **kesimpulan otomatis**. Contoh: "SHORT.md setara dengan SKILL.md, layak dipakai", atau "Skill hampir tidak menaikkan skor dibanding tanpa skill".

### Cara kerja

```
untuk setiap soal × setiap varian (full / short / none):
    1. model target menjawab soal, dengan sisipan sesuai varian
    2. model hakim menilai jawaban 0-10 berdasarkan criteria,
       TANPA diberi tahu varian mana yang menjawab
```

- Setiap baris hasil menyimpan `skill_version` (hash `SKILL.md` + `SHORT.md`), jadi hasil sebelum dan sesudah skill diedit bisa dibandingkan.
- **Biaya:** jumlah soal × jumlah varian × 2 panggilan. Contohnya 20 soal × 3 varian = 120 panggilan, masing-masing sekitar 2.200 token. Jalankan saat skill berubah saja, bukan terus-menerus.
- Eval yang sedang berjalan saat server di-restart akan berhenti di tengah. Hasil yang sudah selesai tetap tersimpan.

### Contoh membaca hasil

Eval `founder-pricing`: full 2,5, short 9, none 7,5. Artinya isi lengkap **memperburuk** jawaban dibanding tanpa skill, sedangkan `SHORT.md` memperbaikinya. Tindakannya: tulis ulang `SKILL.md`, atau jadikan isi `SHORT.md` sebagai skill utama. Penjelasan lengkap ada di [Skill](03-skill.md#skill-yang-tidak-cocok-untuk-middleware-contoh-founder-pricing).
