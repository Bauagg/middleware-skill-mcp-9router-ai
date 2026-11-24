package mcp

import "time"

// FileConfig adalah isi file MCP_CONFIG_PATH (mcp.json).
type FileConfig struct {
	Servers map[string]ServerConfig `json:"servers"`
}

// ServerConfig adalah satu server MCP di file konfigurasi.
type ServerConfig struct {
	// URL endpoint Streamable HTTP, mis. https://mcp.figma.com/mcp
	URL string `json:"url"`

	// Enabled kosong = true.
	Enabled *bool `json:"enabled,omitempty"`

	// Keywords ikut dicocokkan dengan pesan user saat memilih tool (seperti
	// keywords skill), mis. ["desain", "poster"] untuk Canva.
	Keywords []string `json:"keywords,omitempty"`

	// Headers dikirim di setiap request ke server ini, untuk server yang
	// memakai API key, bukan OAuth. Nilai boleh memakai ${NAMA_ENV} supaya
	// rahasianya tetap di .env, mis. {"Authorization": "Bearer ${X_TOKEN}"}.
	Headers map[string]string `json:"headers,omitempty"`
}

// IsEnabled melaporkan apakah server dipakai.
func (c ServerConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// ServerStatus adalah respons GET /api/mcp/servers.
type ServerStatus struct {
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	Own           bool       `json:"own"` // true = ditambahkan user ini; false = bawaan mcp.json
	State         string     `json:"state"`
	Error         string     `json:"error,omitempty"`
	Tools         int        `json:"tools"`
	ReadOnlyTools int        `json:"read_only_tools"`
	LoggedIn      bool       `json:"logged_in"`
	TokenExpires  *time.Time `json:"token_expires_at,omitempty"`
	RefreshedAt   *time.Time `json:"refreshed_at,omitempty"`
	ConnectURL    string     `json:"connect_url"`
}

// ToolInfo adalah satu tool di GET /api/mcp/tools.
type ToolInfo struct {
	Name        string   `json:"name"` // nama yang dilihat model: <server>__<tool>
	Server      string   `json:"server"`
	Tool        string   `json:"tool"` // nama asli di server
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description"`
	ReadOnly    bool     `json:"read_only"`
	Score       *float64 `json:"score,omitempty"`    // terisi bila memakai q
	Selected    *bool    `json:"selected,omitempty"` // akan dikirim ke model untuk pesan q
}

// Connection adalah ringkasan login satu user ke satu server MCP (tanpa token).
type Connection struct {
	UserID      string     `json:"user_id"`
	Server      string     `json:"server"`
	LoggedIn    bool       `json:"logged_in"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

// AllUsersResult adalah respons GET /api/mcp/admin/servers (khusus admin).
type AllUsersResult struct {
	Builtin     []string     `json:"builtin"`      // server bawaan mcp.json (untuk semua user)
	UserServers []UserServer `json:"user_servers"` // server tambahan milik tiap user
	Connections []Connection `json:"connections"`  // siapa sudah login ke server mana
}

// AddServerInput adalah body POST /api/mcp/servers (server milik user).
type AddServerInput struct {
	Name     string   `json:"name" binding:"required,max=40"`
	URL      string   `json:"url" binding:"required,max=2000"`
	Keywords []string `json:"keywords"`
}

// oauthState disimpan di Redis selama login berlangsung (state -> server + PKCE).
type oauthState struct {
	User     string `json:"user"`
	Server   string `json:"server"`
	Verifier string `json:"verifier"`
}
