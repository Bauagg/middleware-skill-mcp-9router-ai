# 10. MCP (Figma, Canva, dan lainnya)

## Kenapa middleware yang memegang MCP

MCP (Model Context Protocol) adalah cara standar memberi model AI akses ke aplikasi lain, misalnya membaca desain Canva atau membuat poster. 9Router hanya meneruskan chat dan tidak bisa terhubung ke server MCP. Karena itu **middleware yang menjadi klien MCP**:

```
Client ──chat──▶ middleware ──(chat + daftar tool)──▶ 9Router ──▶ model
                    │   ▲                                           │
                    │   └──── "panggil tool canva__search_designs" ◀┘
                    ▼
              server MCP (Canva, Figma, ...)
                    │
                    └── hasil tool ──▶ middleware ──(chat + hasil)──▶ 9Router ──▶ model ──▶ jawaban akhir
```

Model di 9Router mendukung tool calling (sudah diuji dengan `AI-Crypro`), jadi model bisa meminta "panggil tool X". Yang menjalankan tool-nya adalah middleware.

## Daftar server: `mcp.json`

Server MCP didaftarkan di file `mcp.json` (lokasi diatur `MCP_CONFIG_PATH`). File ini **tidak berisi rahasia** dan boleh di-commit. Perubahan berlaku setelah server di-restart.

```json
{
  "servers": {
    "canva": {
      "url": "https://mcp.canva.com/mcp",
      "keywords": ["canva", "desain", "poster", "presentasi", "template"]
    },
    "tokoku": {
      "url": "https://mcp.tokoku.example/mcp",
      "headers": { "Authorization": "Bearer ${TOKOKU_TOKEN}" }
    },
    "figma": {
      "url": "https://mcp.figma.com/mcp",
      "enabled": false
    }
  }
}
```

| Field | Wajib | Keterangan |
|---|---|---|
| nama server (kunci) | ya | huruf kecil/angka/`-`/`_`, maks 40. Menjadi awalan nama tool: `canva__search_designs`. |
| `url` | ya | Endpoint MCP **Streamable HTTP**. |
| `enabled` | tidak | `false` = dilewati. Bawaan `true`. |
| `keywords` | tidak | Kata yang ikut dicocokkan dengan pesan user saat memilih tool, seperti `keywords` skill. |
| `headers` | tidak | Header untuk server yang memakai API key, bukan OAuth. `${NAMA_ENV}` diganti isi `.env`, supaya rahasianya tidak tertulis di `mcp.json`. |

Yang didukung hanya server MCP **jarak jauh (HTTP)**. Server MCP lokal yang dijalankan sebagai proses (`npx ...`, stdio) tidak didukung langsung. Jalankan server seperti itu sebagai layanan HTTP terpisah, lalu daftarkan URL-nya.

## Login (OAuth): satu akun bersama

Server seperti Canva memakai login OAuth. Login dilakukan **sekali per server** dan dipakai semua chat, karena memori middleware memang global (tidak terikat user).

1. Buka di browser: `http://<BASE_URL>/api/mcp/servers/canva/connect`
2. Middleware otomatis:
   - mencari alamat login server (discovery, RFC 9728 dan RFC 8414);
   - mendaftarkan dirinya sebagai aplikasi OAuth (Dynamic Client Registration), sekali saja;
   - mengarahkan browser ke halaman login Canva, dengan PKCE.
3. Login dan setujui akses di halaman Canva.
4. Browser kembali ke `/api/mcp/oauth/callback` dan menampilkan "Login berhasil: N tool tersedia".

Token disimpan di tabel `m_mcp_credentials`, **terenkripsi** dengan `MCP_TOKEN_KEY`, lalu diperpanjang otomatis memakai refresh token. Bila perpanjangan gagal (misalnya akses dicabut), server berstatus `needs_login` dan cukup login ulang lewat `/connect`.

Penting:
- `BASE_URL` harus alamat yang bisa dibuka **browser** kamu, karena halaman login mengarahkan balik ke `BASE_URL/api/mcp/oauth/callback`. Di lokal: `http://localhost:4000`. Di VPS: alamat publik lewat NGINX. Bila `BASE_URL` berubah, middleware mendaftar ulang otomatis saat `/connect` berikutnya.
- **Jangan mengganti `MCP_TOKEN_KEY`** setelah login. Token lama tidak bisa dibaca lagi dan semua server harus login ulang.
- Karena `/api/*` tanpa autentikasi, siapa pun yang bisa membuka `/connect` bisa menyambungkan akunnya sendiri. Batasi `/api/*` di NGINX (lihat [deploy](09-menjalankan-dan-deploy.md)).

### Figma belum bisa dipakai lewat server resminya

Server MCP resmi Figma (`mcp.figma.com/mcp`) **menolak pendaftaran otomatis** dengan `403 Forbidden`. Figma hanya mengizinkan aplikasi klien yang sudah mereka setujui. Karena itu `figma` di `mcp.json` diset `enabled: false`.

Jalan keluar yang mungkin:
- jalankan server MCP Figma pihak ketiga yang memakai **personal access token** Figma (misalnya Framelink / `figma-developer-mcp` dalam mode HTTP) sebagai container terpisah, lalu daftarkan URL-nya di `mcp.json`;
- atau ajukan aplikasi ke Figma supaya didaftarkan sebagai klien yang disetujui.

## Memilih tool yang dikirim ke model

Satu server bisa punya puluhan tool, dan setiap definisi tool menambah token. Karena itu tool dipilih **otomatis sesuai pesan**, mirip pemilihan skill:

- Yang dicocokkan (BM25) adalah nama server (×3), `keywords` server (×2), nama dan judul tool (×2), serta deskripsi tool.
- Teks yang dicocokkan adalah **dua pesan user terakhir**, supaya pesan lanjutan seperti "ubah warnanya jadi biru" tetap membawa tool dari topik sebelumnya.
- Yang dikirim hanya tool dengan skor ≥ `MCP_TOOL_MIN_SCORE`, maksimal `MCP_MAX_TOOLS` (8).
- Pesan yang tidak menyinggung tool apa pun (misalnya "halo") **tidak membawa tool** dan diteruskan biasa tanpa tambahan token.
- Bila client sudah mengirim `tools` sendiri, tool MCP tidak ditambahkan, karena tool client harus dijalankan oleh client.

Cek tool mana yang akan terpilih untuk sebuah pesan:

```bash
curl "http://localhost:4000/api/mcp/tools?q=buatkan+poster+promo+di+canva"
```

**Semua tool diizinkan**, termasuk yang mengubah atau menghapus data (sesuai keputusan). Model bisa membuat atau mengubah desain sungguhan, jadi pakai akun yang memang boleh diubah.

## Putaran panggil-tool

Bila ada tool yang ikut, middleware menjalankan putaran ini sendiri:

1. Request dikirim ke 9Router **tanpa streaming**, bersama daftar tool.
2. Bila model meminta tool, semua tool yang diminta dijalankan **bersamaan** di server MCP (batas `MCP_TOOL_TIMEOUT`).
3. Hasilnya (dipotong maks `MCP_TOOL_RESULT_MAX_TOKENS`) ditambahkan ke percakapan, lalu dikirim lagi ke model.
4. Diulang sampai model menjawab tanpa meminta tool, maksimal `MCP_MAX_STEPS` putaran. Putaran terakhir memaksa model menjawab (`tool_choice: none`).
5. Jawaban akhir dikirim ke client:
   - JSON biasa, dengan `usage` berisi **jumlah token semua putaran**;
   - atau SSE bila client meminta `stream: true`. Jawabannya dikirim **sekaligus**, bukan per kata. Selama tool berjalan, client streaming menerima komentar SSE (`: menjalankan tool: ...` dan `: keepalive`) supaya koneksi tidak dianggap mati.

Tool yang gagal tidak menggagalkan request. Kegagalannya dikirim ke model sebagai teks `ERROR: ...`, sehingga model bisa memperbaiki argumen atau menjelaskannya ke user.

Hasil berupa gambar atau audio tidak bisa dilihat model lewat pesan tool. Hasil seperti itu hanya disebutkan sebagai `[gambar image/png, ... byte, tidak ditampilkan]`.

Respons yang membawa tool diberi header `X-MCP-Tools: <jumlah tool yang dikirim>`.

### Hasil uji nyata

Server MCP uji `tokoku` punya tool `get_sales_report`. Pesan: "cek laporan penjualan toko bulan 2026-11, berapa omzetnya?"

| | Hasil |
|---|---|
| Tool yang dipilih | `tokoku__get_sales_report` (skor 1,54) |
| Putaran | 2 (model meminta tool → hasil tool → jawaban) |
| Jawaban | "Omzet toko bulan November 2026 adalah Rp 12.345.000 dari 321 transaksi" |
| Waktu | sekitar 7,5 detik |
| Token prompt | sekitar 5.500 (dua panggilan model, masing-masing dengan tambahan ±2.200 dari 9Router) |

Artinya, setiap pemakaian tool kira-kira **menggandakan biaya dan waktu** dibanding chat biasa. Itu sebabnya tool hanya dikirim bila pesannya memang relevan.

## Status server

`GET /api/mcp/servers`:

| `state` | Arti | Tindakan |
|---|---|---|
| `connected` | Daftar tool berhasil diambil | - |
| `needs_login` | Server menolak (401/403) | Buka `connect_url` di browser |
| `error` | Gagal karena hal lain (lihat `error`) | Periksa URL/jaringan, lalu `POST /api/mcp/servers/:name/refresh` |
| `connecting` | Baru start, belum dicoba | Tunggu beberapa detik |
| `disabled` | `enabled: false` di `mcp.json` | - |

Daftar tool diambil saat start, setelah login, lewat `POST /refresh`, dan otomatis tiap `MCP_REFRESH_INTERVAL` (30 menit).

## Log

Setiap request chat mencatat di `t_usage_logs`:
- `mcp_tools_offered`: jumlah tool yang dikirim ke model;
- `mcp_tools_tokens_est`: perkiraan token definisi tool;
- `mcp_tool_calls` dan `mcp_tool_names`: berapa kali dan tool apa yang dipanggil;
- `mcp_steps`: berapa kali model dipanggil dalam satu request.

Ringkasannya ada di bagian `mcp` pada `GET /api/usage-logs/summary`.

## Menambah server MCP baru

1. Tambahkan entri di `mcp.json` (`url`, dan `keywords` dalam bahasa yang dipakai user).
2. Bila server memakai API key: simpan key-nya di `.env`, lalu rujuk lewat `headers` dengan `${NAMA_ENV}`.
3. Restart server (`docker compose up -d`).
4. Cek `GET /api/mcp/servers`. Bila `needs_login`, buka `connect_url` di browser.
5. Cek pemilihan tool dengan `GET /api/mcp/tools?q=<contoh pesan>`, lalu tambah `keywords` bila tool tidak terpilih.
