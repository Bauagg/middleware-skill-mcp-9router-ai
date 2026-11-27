package aiaccount

import "time"

// Status akun.
const (
	StatusUnchecked  = "unchecked"   // belum pernah dicek
	StatusOK         = "ok"          // sesi berlaku
	StatusNeedsLogin = "needs_login" // sesi/key tidak berlaku: login ulang
	StatusError      = "error"       // gagal dicek karena alasan lain (jaringan, provider)
)

// Provider adalah satu baris tabel m_ai_providers: satu layanan AI yang bisa
// dihubungkan user (Gemini, ChatGPT, atau API kompatibel OpenAI yang
// ditambahkan admin). Cara bicaranya ditentukan Driver.
type Provider struct {
	ID     string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Driver string `json:"driver" gorm:"size:30;not null;index"`
	Name   string `json:"name" gorm:"size:100;not null"`

	Enabled  bool `json:"enabled" gorm:"not null;default:true"`
	Priority int  `json:"priority" gorm:"not null;default:0"` // kecil = dicoba lebih dulu

	// LoginURL mengganti halaman login bawaan driver (kosong = bawaan).
	LoginURL string `json:"login_url" gorm:"type:text;not null;default:''"`
	// Untuk driver API: alamat API (mis. https://api.openai.com/v1), model chat,
	// dan model gambar (kosong = kemampuan itu tidak dipakai).
	BaseURL    string `json:"base_url" gorm:"type:text;not null;default:''"`
	ChatModel  string `json:"chat_model" gorm:"size:200;not null;default:''"`
	ImageModel string `json:"image_model" gorm:"size:200;not null;default:''"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Info driver untuk FE (cara login, kemampuan). Tidak disimpan.
	Info DriverInfo `json:"driver_info" gorm:"-"`
}

// TableName menetapkan nama tabel. Tabel master diberi awalan "m_".
func (Provider) TableName() string { return "m_ai_providers" }

// Account adalah satu baris tabel m_ai_accounts: satu akun user di sebuah
// provider, yang kuotanya dipakai untuk membuat gambar dan video pemiliknya.
// Akun seorang user tidak pernah dipakai untuk request user lain.
type Account struct {
	ID string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	// UserID pemilik. Nama unik per user per provider.
	UserID     string `json:"user_id" gorm:"size:128;not null;default:'';uniqueIndex:idx_m_ai_accounts_user_provider_name,priority:1"`
	ProviderID string `json:"provider_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_m_ai_accounts_user_provider_name,priority:2"`
	Name       string `json:"name" gorm:"size:100;not null;uniqueIndex:idx_m_ai_accounts_user_provider_name,priority:3"`

	// Rahasia akun dalam JSON {nama: nilai} (cookie sesi, atau api_key),
	// terenkripsi MCP_TOKEN_KEY. Tidak pernah dikirim ke client.
	Secret      string `json:"-" gorm:"type:text;not null"`
	CookieCount int    `json:"cookie_count" gorm:"not null;default:0"` // 0 untuk akun API key

	Enabled  bool `json:"enabled" gorm:"not null;default:true"`
	Priority int  `json:"priority" gorm:"not null;default:0"` // kecil = dicoba lebih dulu (di dalam provider)

	Status    string `json:"status" gorm:"size:20;not null;default:'unchecked'"`
	LastError string `json:"last_error" gorm:"type:text;not null;default:''"`

	// Akun dilewati sampai waktu ini setelah limitnya habis.
	ChatCooldownUntil  *time.Time `json:"chat_cooldown_until"`
	ImageCooldownUntil *time.Time `json:"image_cooldown_until"`
	VideoCooldownUntil *time.Time `json:"video_cooldown_until"`

	Chats         int        `json:"chats" gorm:"not null;default:0"` // jawaban chat saat 9Router habis
	Images        int        `json:"images" gorm:"not null;default:0"`
	Videos        int        `json:"videos" gorm:"not null;default:0"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	// Terakhir sesi diperbarui provider (rotasi berkala), tanda sesi masih hidup.
	CookiesUpdatedAt *time.Time `json:"cookies_updated_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName menetapkan nama tabel. Tabel master diberi awalan "m_".
func (Account) TableName() string { return "m_ai_accounts" }

// GeneratedVideo adalah satu baris tabel t_generated_videos: satu video yang
// dibuat lewat chat (tool generate_video). File videonya ada di VideoPath.
type GeneratedVideo struct {
	ID     string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID string `json:"user_id" gorm:"size:128;not null;default:'';index"`

	// URL lengkap saat video dibuat (BASE_URL/videos/<file>) dan nama filenya.
	URL      string `json:"url" gorm:"type:text;not null"`
	FileName string `json:"file_name" gorm:"size:64;not null;uniqueIndex"`

	Prompt    string `json:"prompt" gorm:"type:text;not null"`
	Provider  string `json:"provider" gorm:"size:100;not null;default:''"` // nama provider yang membuatnya
	Account   string `json:"account" gorm:"size:100"`                      // nama akun yang membuatnya
	SizeBytes int    `json:"size_bytes"`
	LatencyMs int64  `json:"latency_ms"`

	CreatedAt time.Time `json:"created_at" gorm:"index"`
}

// TableName menetapkan nama tabel. Tabel transaksi diberi awalan "t_".
func (GeneratedVideo) TableName() string { return "t_generated_videos" }
