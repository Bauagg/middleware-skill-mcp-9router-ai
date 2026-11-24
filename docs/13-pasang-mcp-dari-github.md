# 13. Pasang MCP dari GitHub lewat chat

User bisa meminta lewat chat: *"tolong pasangkan https://github.com/bytebase/dbhub jadi MCP"*. Middleware mengunduh repo, memeriksanya, meminta konfirmasi, meminta persetujuan admin, meminta data rahasia lewat form, lalu menjalankannya di **container Docker terpisah** dan langsung menyambungkannya ke chat.

## Alur

```
user: "pasangkan github.com/bytebase/dbhub"
  │
  ├─ 1. analyze  → repo diunduh ke open_source_mcp/bytebase__dbhub (dikunci ke 1 commit)
  │                dan diperiksa: package.json, pyproject, .env.example, README.
  │                TIDAK ada kode yang dijalankan.
  ├─ 2. AI menjelaskan rencana: nama, perintah install & start, variabel rahasia (mis. DSN)
  │     → user mengonfirmasi
  ├─ 3. submit   → user biasa: status pending_review (menunggu admin)
  │                admin: langsung approved, berlaku untuk SEMUA user,
  │                dan WAJIB memilih sharing atau tidak (AI menanyakannya)
  ├─ 4. link form data rahasia → user mengisi DSN di browser (bukan di chat), disimpan terenkripsi
  ├─ 5. admin menyetujui (chat "setujui pemasangan dbhub" atau API)
  └─ 6. container mcpx-dbhub dibuat → clone commit itu → install → jalan
        → terdaftar sebagai server MCP → tool-nya langsung bisa dipakai di chat
```

## Siapa boleh apa

| | User biasa (termasuk pihak ketiga) | Admin |
|---|---|---|
| Analyze dan submit | ya, menunggu persetujuan admin | ya, langsung disetujui |
| Hasilnya berlaku untuk | hanya dirinya | semua user: sharing atau tidak sharing (lihat di bawah) |
| Approve / reject | tidak | ya |
| Start / stop / remove / log | miliknya sendiri | semua |
| Mengisi data rahasia | miliknya sendiri | miliknya sendiri |

## Sharing atau tidak (khusus pemasangan admin)

Saat admin memasang, AI **wajib** menanyakan pilihan ini. Submit dari admin tanpa `shared` ditolak:

| `shared` | Container | Data rahasia | Cocok untuk |
|---|---|---|---|
| `true` (sharing) | **satu**, `mcpx-<nama>` | milik admin, dipakai semua user | database atau layanan perusahaan yang boleh diakses semua |
| `false` (tidak sharing) | **satu per user**, `mcpx-<nama>-<hash user>` | tiap user mengisi miliknya sendiri | semua user memakai `dbhub`, tapi ke database masing-masing |

Cara kerja tidak sharing:
- Pemasangan admin menjadi **template**: repo, commit, dan perintahnya ditetapkan admin. Template sendiri tidak punya container.
- Template muncul di daftar semua user (*"mcp apa saja yang tersedia?"*). User mulai memakainya dengan meminta link form (`env_link`). Begitu form diisi, container milik user itu langsung jalan tanpa persetujuan lagi, karena kodenya sudah disetujui admin. Bila tidak butuh data rahasia, `env_link` langsung menjalankannya.
- Server yang terdaftar bersifat pribadi: hanya user itu yang melihat dan memakai tool-nya.
- Instance per user disimpan di tabel `m_mcp_install_instances` (data rahasia terenkripsi, status, container).
- User bisa menghentikan atau menghapus **miliknya sendiri**. Admin yang menghapus template ikut menghapus container **semua** user.
- Biayanya: setiap user yang memakai mendapat container sendiri, masing-masing sampai `MCP_RUNNER_MEMORY_MB`. Perhatikan kapasitas VPS.

## Data rahasia

- Chat **tidak pernah** meminta password, DSN, atau token. AI hanya mencatat **nama** variabelnya (mis. `DSN`), lalu memberi **link form** yang ditandatangani dan berlaku 24 jam, khusus untuk user yang memasang.
- Nilainya disimpan terenkripsi (`MCP_TOKEN_KEY`) di `m_mcp_installs.env_enc` dan tidak pernah ditampilkan lagi. API hanya menunjukkan `env_set` dan `env_missing`.
- Nilai diberikan ke container sebagai environment variable. Perintah start bisa merujuknya, mis. `node dist/index.js --transport stdio --dsn "$DSN"`.
- Mengubah data rahasia membuat container dibuat ulang dengan nilai baru.
- Nama yang dilarang: `PATH`, `HOME`, `MCP_*`, dan sejenisnya.

## Isolasi container

Setiap MCP berjalan di container sendiri, `mcpx-<nama>`, dibuat dari image runner `docker/mcp-runner`:

| Pengaman | Isi |
|---|---|
| Tanpa hak istimewa | semua capability dibuang, `no-new-privileges`, bukan root (user `mcp`), tidak privileged |
| Tanpa akses VPS | tidak ada folder host yang dipasang; kode di-clone di dalam container |
| Dibatasi | memori `MCP_RUNNER_MEMORY_MB` (bawaan 1 GB, tanpa swap), CPU `MCP_RUNNER_CPUS`, maks 512 proses |
| Network khusus | hanya `middleware_mcp_net`; tidak tersambung ke network Docker API maupun searxng |
| Env bersih | env middleware (password DB, `JWT_SECRET`, `API_KEYS`, dan lainnya) tidak pernah ikut, hanya variabel `MCP_*` runner dan data rahasia user |
| Versi terkunci | yang dijalankan persis commit yang dianalisis dan disetujui |
| Rusak tidak menular | bila container gagal, ia dihentikan dan log terakhirnya dicatat di `error`; middleware dan VPS tidak terpengaruh |

Middleware tidak memegang socket Docker. Ia memanggil `docker-proxy` (tecnativa/docker-socket-proxy) yang hanya membuka urusan container: buat, jalankan, hentikan, hapus, periksa, dan log. Exec, image, volume, network, dan build ditutup. Proxy itu berada di network internal `docker_api` yang hanya bisa dijangkau middleware.

**Batas yang perlu diketahui:**
- Container MCP tetap bisa mengakses internet (perlu untuk `git`, `npm`, `pip`, dan database user), dan bisa menjangkau port yang dibuka VPS ke semua interface. Pastikan Redis/PostgreSQL di VPS hanya mendengarkan di `127.0.0.1` atau dijaga password.
- Bila middleware sendiri dibobol, penyerang bisa membuat container lewat proxy. Inilah sebabnya pengajuan user biasa wajib disetujui admin, dan admin perlu membaca repo serta perintahnya sebelum menyetujui.

## Cara container menjalankan repo

Image runner berisi git, Node 22 (npm, pnpm, dan yarn lewat corepack), Python 3, uv, build-essential, dan [supergateway](https://github.com/supercorp-ai/supergateway). Container menjalankan:

1. `git fetch` commit yang disetujui ke `/app`;
2. `install_command`, mis. `pnpm install && pnpm build` (hanya sekali per commit; restart tidak meng-install ulang);
3. `start_command`:
   - `transport: stdio` (bawaan): dibungkus supergateway menjadi Streamable HTTP di port 8000 path `/mcp`;
   - `transport: http`: server sendiri harus mendengarkan `0.0.0.0:8000` di `http_path`.

Middleware menunggu sampai server menjawab daftar tool, maksimal `MCP_RUNNER_START_TIMEOUT` (bawaan 10 menit), lalu mendaftarkannya ke `m_mcp_servers` (`installed = true`). Server ini tidak bisa dihapus lewat `remove_mcp_server`; pakai `manage_mcp_install`.

Saat middleware start, pemasangan yang tadinya berjalan didaftarkan ulang, atau dijalankan lagi bila container-nya hilang. Container memakai `restart: unless-stopped`, jadi ikut hidup lagi setelah VPS reboot.

## Tool chat

| Tool | Action |
|---|---|
| `install_mcp_from_github` | `analyze` (unduh dan periksa), `submit` (ajukan dengan perintah yang sudah dikonfirmasi user) |
| `manage_mcp_install` | `list`, `env_link`, `start`, `stop`, `remove`, dan khusus admin: `approve`, `reject` |

Contoh: *"mcp apa saja yang saya pasang dari github?"*, *"setujui pemasangan dbhub"* (admin), *"stop mcp dbhub"*.

## API

Semua butuh identitas user. User biasa hanya melihat miliknya, admin melihat semua.

| Method | URL | Keterangan |
|---|---|---|
| POST | `/api/mcp/installs/analyze` | `{"repo_url"}` → hasil pemeriksaan repo |
| POST | `/api/mcp/installs` | Ajukan: `{"repo_url","name","install_command","start_command","transport","http_path","env_keys","keywords","purpose","shared"}` (`shared` wajib untuk admin) |
| GET | `/api/mcp/installs?status=` | Daftar pemasangan: milik sendiri ditambah MCP dari admin (dengan `my_instance` untuk yang tidak sharing) |
| GET | `/api/mcp/installs/:id` | Detail (termasuk `env_url` bila kamu pemiliknya) |
| POST | `/api/mcp/installs/:id/approve` | Setujui (admin) |
| POST | `/api/mcp/installs/:id/reject` | Tolak (admin): `{"reason"}` |
| POST | `/api/mcp/installs/:id/start` | Jalankan lagi (tidak sharing: server milik sendiri) |
| POST | `/api/mcp/installs/:id/stop` | Hentikan dan lepas dari chat (tidak sharing: milik sendiri) |
| DELETE | `/api/mcp/installs/:id` | Hapus container, server, dan data. Tidak sharing: user menghapus miliknya; admin menghapus template beserta semua instance |
| GET | `/api/mcp/installs/:id/logs` | 200 baris log terakhir container |
| GET/POST | `/api/mcp/installs/:id/env?u=&exp=&sig=` | Halaman form data rahasia (link dari chat) |

Status: `pending_review` → `approved` → `starting` → `running`, atau `failed` / `rejected` / `stopped`.

## Menyalakan

Di VPS (docker-compose) fitur ini **aktif otomatis**. `docker compose up -d --build` sekaligus:
- membangun image runner (service `mcp-runner`, langsung selesai setelah build);
- menjalankan `docker-proxy`;
- membuat network `middleware_mcp_net`;
- memasang volume `mcp_repos_data` untuk `open_source_mcp`.

Syarat: `MCP_TOKEN_KEY` terisi.

Di laptop (`go run`):
1. Docker Desktop > Settings > General > centang **Expose daemon on tcp://localhost:2375 without TLS**.
2. `docker network create middleware_mcp_net`
3. `docker build -t middleware-mcp-runner:latest docker/mcp-runner`
4. Di `.env`: `MCP_DOCKER_HOST=tcp://localhost:2375` dan `MCP_RUNNER_PUBLISH_HOST=127.0.0.1` (container dijangkau lewat port di `127.0.0.1`).

`MCP_DOCKER_HOST` kosong berarti fitur mati: tool tidak ditawarkan di chat, dan API menjawab bahwa fitur belum diaktifkan.
