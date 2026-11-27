package aiaccount

import (
	"context"
	"errors"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
)

// Kesalahan yang dibedakan Service untuk memilih tindakan (login ulang, jeda
// akun, coba akun lain). Driver membungkus kesalahan kliennya dengan
// classify supaya errors.Is mengenalinya tanpa kehilangan pesan aslinya.
var (
	// ErrAuth: sesi/kredensial tidak berlaku; perlu login ulang atau key baru.
	ErrAuth = errors.New("sesi tidak berlaku: login ulang")
	// ErrUsageLimit: kuota akun untuk fitur ini habis.
	ErrUsageLimit = errors.New("limit akun habis")
	// ErrBlocked: provider membatasi server ini sementara.
	ErrBlocked = errors.New("provider membatasi server ini sementara")
	// ErrUnsupported: driver tidak mendukung fitur ini.
	ErrUnsupported = errors.New("provider ini tidak mendukung fitur ini")
)

type classified struct{ kind, err error }

func (e *classified) Error() string   { return e.err.Error() }
func (e *classified) Unwrap() []error { return []error{e.kind, e.err} }

// classify menandai err sebagai salah satu kesalahan di atas.
func classify(kind, err error) error { return &classified{kind: kind, err: err} }

// Cara akun sebuah provider dihubungkan.
const (
	AuthBrowser = "browser" // login di browser server (layar noVNC), sesi = cookie
	AuthAPIKey  = "api_key" // user menempel API key
)

// DriverInfo menjelaskan satu driver ke FE: cara login, kemampuan, dan isian
// yang dibutuhkan saat admin menambah provider.
type DriverInfo struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Auth        string `json:"auth"`
	// Kemampuan: chat teks (cadangan saat 9Router habis), gambar, video.
	Text  bool `json:"text"`
	Image bool `json:"image"`
	Video bool `json:"video"`
	// Note: keterangan tambahan untuk user, mis. fitur yang belum tersedia.
	Note string `json:"note,omitempty"`

	// Untuk AuthBrowser: halaman login bawaan, domain cookie yang disimpan,
	// dan cookie penanda sudah login (salah satu harus ada; akhiran "*" = awalan).
	LoginURL       string   `json:"login_url,omitempty"`
	CookieDomains  []string `json:"cookie_domains,omitempty"`
	SessionCookies []string `json:"session_cookies,omitempty"`

	// Isian provider yang diisi admin: alamat API, dan model (chat dan/atau
	// gambar; kemampuan yang modelnya kosong tidak dipakai).
	NeedsBaseURL bool `json:"needs_base_url"`
	ModelFields  bool `json:"model_fields"`

	// Builtin: satu provider dibuat otomatis untuk driver ini dan tidak bisa dihapus.
	Builtin bool `json:"builtin"`
}

// Driver adalah cara bicara dengan satu jenis layanan AI. Menambah layanan
// AI baru (mis. Grok) cukup dengan satu Driver baru yang didaftarkan di
// NewService; akun, login, rotasi sesi, cooldown, dan FE memakainya otomatis.
type Driver interface {
	Info() DriverInfo
	// Open membuat sesi dari rahasia akun (cookie, atau {"api_key": ...}).
	// Belum ada request ke provider.
	Open(p *Provider, secret map[string]string) Session
}

// Session adalah satu akun yang terhubung. Harus aman dipakai bersamaan.
type Session interface {
	// Check memastikan sesi berlaku (dipakai saat akun ditambahkan/dicek).
	Check(ctx context.Context) error
	// Refresh menjaga sesi tetap hidup; dipanggil berkala walau akun jarang dipakai.
	Refresh(ctx context.Context) error
	// Secret mengembalikan rahasia terbaru dan apakah berubah sejak pemanggilan
	// sebelumnya (mis. cookie yang diperbarui provider), supaya disimpan ulang.
	Secret() (map[string]string, bool)
	// Chat menjawab percakapan (pesan terakhir dari user) dengan teks.
	Chat(ctx context.Context, messages []clients.ChatMessage) (string, error)
	// Image membuat satu gambar.
	Image(ctx context.Context, prompt, size string) (*clients.GeneratedImage, error)
	// Video membuat satu video (mp4).
	Video(ctx context.Context, prompt string) ([]byte, error)
}

// emailer dipenuhi sesi yang tahu email akunnya (dipakai sebagai nama akun
// otomatis setelah login).
type emailer interface{ Email() string }

// noVideo bisa disematkan driver yang tidak membuat video.
type noVideo struct{}

func (noVideo) Video(context.Context, string) ([]byte, error) { return nil, ErrUnsupported }
