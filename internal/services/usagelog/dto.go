package usagelog

import "time"

// Entry adalah satu catatan pemakaian yang dikirim proxy.
type Entry struct {
	Model  string
	Path   string
	Stream bool

	SkillName      *string
	SkillScore     *float64
	ExtraTokensEst int
	SkillVariant   *string
	BudgetTokens   int

	SelectionCache string
	SessionCache   string

	SelectionMethod string
	FallbackTokens  int

	ContextPackTokensEst int
	MemoryNotes          int
	MemoryNoteIDs        string
	MemoryTokensEst      int

	MCPToolsOffered   int
	MCPToolsTokensEst int
	MCPToolCalls      int
	MCPToolNames      string
	MCPSteps          int

	Status           int
	PromptTokens     *int
	CompletionTokens *int
	CachedTokens     *int
	LatencyMs        int64
	Aborted          bool
}

// ListFilter adalah parameter daftar log.
type ListFilter struct {
	Model string
	Skill string
	Page  int
	Limit int
}

// SummaryFilter membatasi rentang waktu ringkasan (nil = tanpa batas).
type SummaryFilter struct {
	From *time.Time
	To   *time.Time
}

// SummaryResult adalah ringkasan pemakaian pada rentang waktu tertentu.
type SummaryResult struct {
	// Skills: ringkasan per skill ("" = request tanpa skill).
	Skills []SummaryRow `json:"skills"`

	// Jumlah request per status cache ("none" = tidak berlaku). Hit rate =
	// hit / (hit + miss).
	SelectionCache map[string]int64 `json:"selection_cache"`
	SessionCache   map[string]int64 `json:"session_cache"`

	// Variants: berapa kali tiap varian teks skill dipakai (full/short/truncated).
	Variants map[string]int64 `json:"variants"`

	// Methods: berapa request per metode seleksi (bm25/embedding/fallback).
	Methods map[string]int64 `json:"selection_methods"`

	// Memory: seberapa sering context pack dan catatan memori ikut disisipkan.
	Memory MemorySummary `json:"memory"`

	// MCP: seberapa sering tool MCP dikirim ke model dan dipanggil.
	MCP MCPSummary `json:"mcp"`
}

// MCPSummary meringkas pemakaian tool MCP. Rata-rata hanya dihitung dari
// request yang memang membawa tool / memanggil tool.
type MCPSummary struct {
	WithToolsOffered  int64   `json:"with_tools_offered"`
	WithToolCalls     int64   `json:"with_tool_calls"`
	AvgToolsTokensEst float64 `json:"avg_tools_tokens_est"`
	AvgToolCalls      float64 `json:"avg_tool_calls"`
	AvgStepsWithCalls float64 `json:"avg_steps_with_calls"`
}

// MemorySummary meringkas penyisipan memori (Tahap 4). Rata-rata token hanya
// dihitung dari request yang memang disisipi.
type MemorySummary struct {
	WithContextPack         int64   `json:"with_context_pack"`
	WithMemoryNotes         int64   `json:"with_memory_notes"`
	AvgContextPackTokensEst float64 `json:"avg_context_pack_tokens_est"`
	AvgMemoryTokensEst      float64 `json:"avg_memory_tokens_est"`
	AvgMemoryNotes          float64 `json:"avg_memory_notes"`
}

// SummaryRow adalah ringkasan pemakaian untuk satu skill.
type SummaryRow struct {
	Skill     string `json:"skill"`      // "" = request tanpa skill
	Requests  int64  `json:"requests"`   // jumlah request
	WithUsage int64  `json:"with_usage"` // yang token-nya dilaporkan 9Router

	AvgExtraTokensEst   float64  `json:"avg_extra_tokens_est"`
	AvgPromptTokens     *float64 `json:"avg_prompt_tokens"`
	AvgCompletionTokens *float64 `json:"avg_completion_tokens"`
	AvgCachedTokens     *float64 `json:"avg_cached_tokens"`
	AvgLatencyMs        float64  `json:"avg_latency_ms"`
}
