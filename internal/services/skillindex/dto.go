package skillindex

// MatchCandidate adalah satu skill hasil pencarian beserta skornya.
type MatchCandidate struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Score       float64 `json:"score"`
}

// MatchResult adalah hasil pemeriksaan pemilihan skill untuk satu query.
type MatchResult struct {
	Query string `json:"query"`

	// Method: "bm25" atau "embedding"; "fallback" bila pemilih cadangan ikut
	// dijalankan (fallback=true) dan berhasil memutuskan.
	Method string `json:"method"`

	// Threshold adalah batas skor untuk metode itu (SKILL_MIN_SCORE atau
	// SKILL_MIN_SIMILARITY).
	Threshold float64 `json:"threshold"`

	// Ambiguous true bila skor ambigu; pada request sungguhan, pemilih
	// cadangan akan dipanggil (bila FallbackEnabled).
	Ambiguous       bool `json:"ambiguous"`
	FallbackEnabled bool `json:"fallback_enabled"`
	FallbackTokens  int  `json:"fallback_tokens"`

	// Selected adalah skill yang akan disisipkan, null bila tidak ada yang cocok.
	Selected   *string          `json:"selected"`
	Candidates []MatchCandidate `json:"candidates"`
}
