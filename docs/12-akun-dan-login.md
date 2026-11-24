# 12. Akun, login, dan role

Middleware mengenali pemanggil dengan **dua cara**. Keduanya bisa dipakai bersamaan oleh aplikasi berbeda.

| Cara | Untuk siapa | Header | `user_id` | Role |
|---|---|---|---|---|
| **Aplikasi pihak ketiga** | Aplikasi yang sudah punya user sendiri (mis. Luma) | `X-API-Key` + `X-User-Id` | isi `X-User-Id` | selalu `user` |
| **Akun middleware** | User yang tidak punya aplikasi pihak ketiga, mis. lewat web middleware AI | `Authorization: Bearer <access_token>` | `id` di tabel `m_users` | `user` atau `admin` |

Dengan token login, header `X-User-Id` **diabaikan**: `user_id` selalu diambil dari token, jadi user tidak bisa menyamar sebagai user lain. Semua tabel lain tetap memakai kolom `user_id` yang sama, apa pun cara masuknya.

Header 9Router (`X-Router-URL`, `X-Router-Key`) dan field `model` tetap wajib untuk chat, apa pun cara masuknya.

## Tabel `m_users`

| Kolom | Isi |
|---|---|
| `id` | UUID, dipakai sebagai `user_id` di semua tabel |
| `email` | unik, disimpan huruf kecil |
| `password` | hash bcrypt (tidak pernah dikirim di respons) |
| `role` | `user` atau `admin` |

Tabel dibuat otomatis saat server start.

## Role

| Role | Siapa | Data yang terlihat |
|---|---|---|
| `admin` | Pemilik aplikasi | **Semua user**: context pack, catatan memori, gambar, log pemakaian, eval, dan server MCP. Boleh membuka, mengubah, dan menghapus milik user lain. |
| `user` | Akun yang register, dan **semua** user dari aplikasi pihak ketiga | Hanya miliknya sendiri |

- **Skill selalu global**: semua role melihat dan memakai skill yang sama.
- **Server MCP buatan admin juga global**: berlaku untuk semua user, dengan login bersama milik admin atau login masing-masing (lihat [MCP](10-mcp.md#server-dari-admin-untuk-semua-user)). Server MCP buatan user biasa tetap pribadi. MCP dari GitHub yang dipasang admin juga global, dengan pilihan sharing atau tidak sharing (lihat [Pasang MCP dari GitHub](13-pasang-mcp-dari-github.md#sharing-atau-tidak-khusus-pemasangan-admin)).
- **Chat tetap pribadi**: saat admin chat, yang disisipkan tetap context pack dan catatan milik admin sendiri, bukan milik semua user.
- Data baru (context pack, catatan, dan lainnya) selalu tercatat atas nama pembuatnya.

### Membuat admin

Register **selalu** menghasilkan role `user`; field `role` di body diabaikan. Register terbuka tanpa key/token, jadi role admin sengaja tidak bisa dipilih di sana. Ada dua cara membuat admin:

**1. Langsung di database:** register seperti biasa, lalu naikkan role-nya di PostgreSQL.

```sql
UPDATE m_users SET role = 'admin' WHERE email = 'pemilik@contoh.com';
-- menurunkan lagi: UPDATE m_users SET role = 'user' WHERE email = 'pemilik@contoh.com';
```

Role tersimpan di dalam token, jadi perubahan baru berlaku setelah user **login ulang** atau **refresh token**. Access token lama tetap membawa role lama sampai kedaluwarsa, paling lama `JWT_ACCESS_TTL` (15 menit). Email tersimpan huruf kecil.

**2. Dari `.env` saat server start:**

```env
ADMIN_EMAIL=pemilik@contoh.com
ADMIN_PASSWORD=password-yang-kuat
```

- Bila email belum terdaftar, akun admin dibuat.
- Bila email sudah terdaftar dengan **password yang sama**, akun itu dinaikkan jadi admin.
- Bila email sudah dipakai orang lain dengan password berbeda, akun itu **tidak** dijadikan admin. Log mencatat peringatan.

Setelah admin dibuat, `ADMIN_PASSWORD` boleh dikosongkan lagi.

## Endpoint

Register, login, dan refresh **tidak** butuh `X-API-Key`, karena dipanggil dari browser.

| Method | URL | Body | Hasil |
|---|---|---|---|
| POST | `/api/auth/register` | `{"email","password"}` (password 8–72 karakter) | 201 + akun (selalu role `user`) + token. 409 bila email sudah terdaftar. |
| POST | `/api/auth/login` | `{"email","password"}` | 200 + akun + token. 401 `Email atau password salah`. |
| POST | `/api/auth/refresh` | `{"refresh_token"}` | 200 + pasangan token baru. 401 bila tidak valid atau kedaluwarsa. |
| GET | `/api/auth/me` | header `Authorization: Bearer <access_token>` | akun yang sedang login |

Respons register, login, dan refresh:

```json
{
  "status": "success",
  "data": {
    "user": { "id": "3f1c...", "email": "budi@contoh.com", "role": "user" },
    "access_token": "eyJ...",
    "refresh_token": "eyJ...",
    "token_type": "Bearer",
    "expires_in": 900,
    "refresh_expires_in": 2592000
  }
}
```

```bash
curl -X POST http://localhost:4000/api/auth/register -H "Content-Type: application/json" \
  -d '{"email":"budi@contoh.com","password":"rahasia123"}'

# chat dengan token login (tanpa X-API-Key dan X-User-Id)
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer <access_token>" \
  -H "X-Router-URL: https://<domain-9router>/v1" -H "X-Router-Key: <KEY 9ROUTER>" \
  -H "Content-Type: application/json" \
  -d '{"model":"AI-Crypro","messages":[{"role":"user","content":"halo"}]}'
```

SDK OpenAI bisa langsung dipakai: isi `api_key` dengan access token. SDK mengirimnya sebagai `Authorization: Bearer`.

## Token

| Token | Umur bawaan | Dipakai untuk |
|---|---|---|
| Access token | 15 menit (`JWT_ACCESS_TTL`) | Header `Authorization: Bearer` di setiap request |
| Refresh token | 30 hari (`JWT_REFRESH_TTL`) | Hanya `POST /api/auth/refresh` |

- Keduanya JWT HS256 yang ditandatangani `JWT_SECRET`. Refresh token ditolak bila dipakai sebagai access token, dan sebaliknya.
- Saat refresh, role dibaca ulang dari database, jadi perubahan role berlaku di token berikutnya.
- Refresh token **tidak disimpan di database**, sesuai permintaan bahwa tabel user hanya berisi 4 kolom. Akibatnya, token tidak bisa dicabut satu per satu sebelum kedaluwarsa. Untuk mencabut **semua** token sekaligus, ganti `JWT_SECRET`; semua user harus login ulang.
- Access token yang kedaluwarsa dijawab 401 `access token tidak valid atau kedaluwarsa`. Web cukup memanggil `/api/auth/refresh`, lalu mengulang request.

## Header `Authorization` di `/v1/*`

SDK OpenAI selalu mengirim `Authorization: Bearer <api_key>`. Middleware hanya menganggapnya token login bila isinya berbentuk JWT (tiga bagian dipisah titik). Isi lain, misalnya `sk-...`, diabaikan dan pemeriksaan pindah ke `X-API-Key`. Header ini tidak pernah diteruskan ke 9Router: middleware menggantinya dengan key dari `X-Router-Key`.
