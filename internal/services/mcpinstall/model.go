package mcpinstall

import "time"

// Status pemasangan MCP dari GitHub.
const (
	StatusPendingReview = "pending_review" // diajukan user biasa, menunggu persetujuan admin
	StatusApproved      = "approved"       // disetujui; dijalankan begitu data rahasia lengkap
	StatusStarting      = "starting"       // container dibuat, kode di-clone dan di-install
	StatusRunning       = "running"        // berjalan dan terdaftar sebagai server MCP
	StatusFailed        = "failed"         // gagal dijalankan (lihat Error dan log)
	StatusRejected      = "rejected"       // ditolak admin
	StatusStopped       = "stopped"        // dihentikan
)

// Install adalah satu baris tabel m_mcp_installs: server MCP dari repo GitHub
// yang dipasang lewat chat dan dijalankan di container terpisah. Kode dikunci
// ke satu commit (CommitSHA): yang dijalankan persis yang diperiksa dan
// disetujui admin.
type Install struct {
	ID string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	// UserID yang meminta. Pemasangan oleh admin berlaku untuk semua user (Global).
	UserID string `json:"user_id" gorm:"size:128;not null;index"`
	// Name adalah nama server MCP (juga nama container mcpx-<name>), unik.
	Name string `json:"name" gorm:"size:40;not null;uniqueIndex"`

	RepoURL   string `json:"repo_url" gorm:"type:text;not null"`
	Owner     string `json:"owner" gorm:"size:100;not null"`
	Repo      string `json:"repo" gorm:"size:100;not null"`
	CommitSHA string `json:"commit_sha" gorm:"size:40;not null"`
	Runtime   string `json:"runtime" gorm:"size:20"` // hasil deteksi: node | python | lainnya

	// perintah yang dijalankan di container (diusulkan AI, dikonfirmasi user,
	// disetujui admin)
	InstallCommand string `json:"install_command" gorm:"type:text"`
	StartCommand   string `json:"start_command" gorm:"type:text;not null"`
	// Transport: stdio (dibungkus supergateway jadi HTTP) atau http (server
	// sendiri mendengarkan di port 8000 pada HTTPPath).
	Transport string `json:"transport" gorm:"size:10;not null;default:'stdio'"`
	HTTPPath  string `json:"http_path" gorm:"size:100;not null;default:'/mcp'"`

	Keywords string `json:"-" gorm:"type:text"` // JSON array
	EnvKeys  string `json:"-" gorm:"type:text"` // JSON array nama variabel rahasia yang dibutuhkan
	// EnvEnc adalah isi variabel rahasia (JSON map) terenkripsi MCP_TOKEN_KEY.
	// Tidak pernah dikirim di respons.
	EnvEnc  string `json:"-" gorm:"type:text"`
	Purpose string `json:"purpose" gorm:"type:text"`

	Global bool `json:"global" gorm:"not null;default:false"`
	// PerUser (hanya untuk Global buatan admin): tidak sharing. Pemasangan ini
	// jadi template; tiap user mengisi data rahasianya sendiri dan mendapat
	// container sendiri (tabel m_mcp_install_instances). false = sharing: satu
	// container berisi data rahasia admin dipakai semua user.
	PerUser bool   `json:"per_user" gorm:"not null;default:false"`
	Status  string `json:"status" gorm:"size:20;not null;index"`
	Error   string `json:"error,omitempty" gorm:"type:text"`

	ReviewedBy string     `json:"reviewed_by,omitempty" gorm:"size:128"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`

	ContainerID string `json:"container_id,omitempty" gorm:"size:80"`
	Endpoint    string `json:"endpoint,omitempty" gorm:"size:300"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName menetapkan nama tabel. Data master diberi awalan "m_".
func (Install) TableName() string { return "m_mcp_installs" }

// Instance adalah satu baris tabel m_mcp_install_instances: pemakaian template
// tidak-sharing oleh satu user, dengan data rahasia dan container miliknya
// sendiri (mcpx-<nama>-<hash user>). Kodenya sudah disetujui admin, jadi
// tidak perlu persetujuan lagi.
type Instance struct {
	ID        string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	InstallID string `json:"install_id" gorm:"type:uuid;not null;uniqueIndex:idx_m_mcp_install_instances_user,priority:1"`
	UserID    string `json:"user_id" gorm:"size:128;not null;uniqueIndex:idx_m_mcp_install_instances_user,priority:2"`

	EnvEnc string `json:"-" gorm:"type:text"` // data rahasia user, terenkripsi MCP_TOKEN_KEY
	Status string `json:"status" gorm:"size:20;not null;index"`
	Error  string `json:"error,omitempty" gorm:"type:text"`

	ContainerID string `json:"container_id,omitempty" gorm:"size:80"`
	Endpoint    string `json:"endpoint,omitempty" gorm:"size:300"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName menetapkan nama tabel.
func (Instance) TableName() string { return "m_mcp_install_instances" }
