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
// OAuth dan token login satu user untuk satu server MCP. Tiap user login ke
// akunnya sendiri. Secret dan token disimpan terenkripsi (MCP_TOKEN_KEY).
type Credential struct {
	ID     string `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID string `gorm:"size:128;not null;default:'';uniqueIndex:idx_m_mcp_credentials_user_server,priority:1"`
	Server string `gorm:"size:100;not null;uniqueIndex:idx_m_mcp_credentials_user_server,priority:2"`

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

// UserServer adalah satu baris tabel m_mcp_servers: server MCP yang ditambahkan
// lewat chat atau API. Buatan user biasa hanya terlihat oleh user itu. Buatan
// admin (Global) berlaku untuk SEMUA user, karena admin yang mengatur
// middleware. Server bawaan dari file ada di mcp.json.
type UserServer struct {
	ID       string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID   string `json:"user_id" gorm:"size:128;not null;uniqueIndex:idx_m_mcp_servers_user_name,priority:1"`
	Name     string `json:"name" gorm:"size:40;not null;uniqueIndex:idx_m_mcp_servers_user_name,priority:2"`
	URL      string `json:"url" gorm:"type:text;not null"`
	Keywords string `json:"keywords" gorm:"type:text"` // daftar kata, JSON array

	// Global: dibuat admin, terlihat oleh semua user (UserID = admin pembuatnya).
	Global bool `json:"global" gorm:"not null;default:false;index"`
	// SharedLogin (hanya untuk Global): semua user memakai login admin pembuatnya,
	// mis. GitHub/database organisasi. false = tiap user login ke akunnya sendiri.
	SharedLogin bool `json:"shared_login" gorm:"not null;default:false"`
	// Installed: server hasil clone GitHub yang berjalan di container (diurus
	// lewat m_mcp_installs); dihapus lewat pengelola install, bukan di sini.
	Installed bool `json:"installed" gorm:"not null;default:false"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName menetapkan nama tabel. Data master diberi awalan "m_".
func (UserServer) TableName() string { return "m_mcp_servers" }
