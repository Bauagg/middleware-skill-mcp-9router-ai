package mcp

import (
	"encoding/json"
	"time"
)

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

// ToolSpec adalah tool yang dikirim ke model: definisi function berformat
// OpenAI ({"type":"function","function":{...}}).
type ToolSpec struct {
	Name       string
	Server     string
	Definition json.RawMessage
}

// CallResult adalah hasil pemanggilan tool untuk diberikan ke model.
type CallResult struct {
	Text    string
	IsError bool
}

// oauthState disimpan di Redis selama login berlangsung (state -> server + PKCE).
type oauthState struct {
	Server   string `json:"server"`
	Verifier string `json:"verifier"`
}
