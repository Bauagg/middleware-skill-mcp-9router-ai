package usagelog

import "time"

// UsageLog adalah satu baris tabel t_usage_logs: satu request ke
// /v1/chat/completions. Kolom token bernilai NULL bila 9Router tidak
// melaporkannya (berbeda dengan 0), supaya rata-rata tidak menyesatkan.
type UsageLog struct {
	ID string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`

	// UserID pengirim request (header X-User-Id).
	UserID string `json:"user_id" gorm:"size:128;not null;default:'';index"`

	Model  string `json:"model" gorm:"size:200;index"`
	Path   string `json:"path" gorm:"size:100"`
	Stream bool   `json:"stream"`

	// SkillName NULL = tidak ada skill yang disisipkan. SkillScore adalah skor
	// BM25 saat dipilih (untuk melacak kenapa skill itu terpilih).
	SkillName  *string  `json:"skill_name" gorm:"size:200;index"`
	SkillScore *float64 `json:"skill_score"`

	// ExtraTokensEst adalah perkiraan token tambahan dari skill (karakter / 4),
	// bukan hitungan tokenizer.
	ExtraTokensEst int `json:"extra_tokens_est"`

	// SkillVariant: "full", "short", atau "truncated" (NULL bila tanpa skill).
	// BudgetTokens adalah batas token skill untuk model ini (0 = tanpa batas).
	SkillVariant *string `json:"skill_variant" gorm:"size:20"`
	BudgetTokens int     `json:"budget_tokens"`

	// Hasil cache: "hit", "miss", "off", dan khusus sesi juga "switch" (skill
	// aktif diganti). Kosong bila tidak berlaku (mis. pesan tanpa kata isi).
	SelectionCache string `json:"selection_cache" gorm:"size:10"`
	SessionCache   string `json:"session_cache" gorm:"size:10"`

	// SelectionMethod: "bm25", "embedding", atau "fallback" (diputuskan pemilih
	// cadangan karena skor ambigu). FallbackTokens adalah token yang dipakai
	// pemilih cadangan (0 bila tidak dipanggil), supaya biayanya terlihat.
	SelectionMethod string `json:"selection_method" gorm:"size:20"`
	FallbackTokens  int    `json:"fallback_tokens"`

	// Memori (Tahap 4): perkiraan token context pack dan catatan memori yang
	// disisipkan, jumlah catatan, dan id-nya (dipisah koma) supaya bisa dilacak
	// catatan mana yang memengaruhi jawaban.
	// Kolom ini ditambahkan belakangan: not null + default supaya baris lama
	// terisi 0/'' (bukan NULL yang gagal dibaca ke int/string).
	ContextPackTokensEst int    `json:"context_pack_tokens_est" gorm:"not null;default:0"`
	MemoryNotes          int    `json:"memory_notes" gorm:"not null;default:0"`
	MemoryNoteIDs        string `json:"memory_note_ids" gorm:"type:text;not null;default:''"`
	MemoryTokensEst      int    `json:"memory_tokens_est" gorm:"not null;default:0"`

	// MCP: berapa tool yang dikirim ke model (dan perkiraan tokennya), berapa
	// kali tool dipanggil, nama tool-nya (dipisah koma), dan berapa kali model
	// dipanggil dalam satu request (1 = tanpa putaran tool).
	MCPToolsOffered   int    `json:"mcp_tools_offered" gorm:"column:mcp_tools_offered;not null;default:0"`
	MCPToolsTokensEst int    `json:"mcp_tools_tokens_est" gorm:"column:mcp_tools_tokens_est;not null;default:0"`
	MCPToolCalls      int    `json:"mcp_tool_calls" gorm:"column:mcp_tool_calls;not null;default:0"`
	MCPToolNames      string `json:"mcp_tool_names" gorm:"column:mcp_tool_names;type:text;not null;default:''"`
	MCPSteps          int    `json:"mcp_steps" gorm:"column:mcp_steps;not null;default:0"`

	Status           int   `json:"status"`
	PromptTokens     *int  `json:"prompt_tokens"`
	CompletionTokens *int  `json:"completion_tokens"`
	CachedTokens     *int  `json:"cached_tokens"`
	LatencyMs        int64 `json:"latency_ms"`
	Aborted          bool  `json:"aborted"` // client memutus koneksi sebelum respons selesai

	CreatedAt time.Time `json:"created_at" gorm:"index"`
}

// TableName menetapkan nama tabel. Tabel transaksi diberi awalan "t_".
func (UsageLog) TableName() string { return "t_usage_logs" }
