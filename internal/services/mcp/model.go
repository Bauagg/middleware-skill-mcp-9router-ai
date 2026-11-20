package mcp

import "time"

// Status koneksi server MCP.
const (
	StateDisabled   = "disabled"    // enabled: false di file konfigurasi
	StateConnecting = "connecting"  // belum pernah dicoba / sedang mengambil daftar tool
	StateNeedsLogin = "needs_login" // server menolak (401/403): login lewat /connect
	StateConnected  = "connected"   // daftar tool berhasil diambil
	StateError      = "error"       // gagal karena hal lain (lihat field error)
)

// Credential adalah satu baris tabel m_mcp_credentials: pendaftaran klien
// OAuth dan token login untuk satu server MCP. Satu server = satu akun bersama
// (tidak terikat user). Secret dan token disimpan terenkripsi (MCP_TOKEN_KEY).
type Credential struct {
	ID     string `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Server string `gorm:"size:100;not null;uniqueIndex"`

	// hasil discovery dan pendaftaran klien (DCR); dibuat ulang bila alamat
	// login atau redirect berubah
	ClientID        string `gorm:"size:500"`
	ClientSecretEnc string `gorm:"type:text"`
	AuthMethod      string `gorm:"size:40"`
	AuthURL         string `gorm:"size:1000"`
	TokenURL        string `gorm:"size:1000"`
	Resource        string `gorm:"size:1000"`
	Scopes          string `gorm:"type:text"` // dipisah spasi
	RedirectURI     string `gorm:"size:1000"`

	// token login (kosong = belum login)
	AccessTokenEnc  string `gorm:"type:text"`
	RefreshTokenEnc string `gorm:"type:text"`
	TokenType       string `gorm:"size:40"`
	ExpiresAt       *time.Time
	ConnectedAt     *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName menetapkan nama tabel. Data master diberi awalan "m_".
func (Credential) TableName() string { return "m_mcp_credentials" }
