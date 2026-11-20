package memorynote

// CreateInput adalah body POST /api/memory-notes (catatan manual, langsung aktif).
type CreateInput struct {
	Topic        string `json:"topic" binding:"required,max=200"`
	Tried        string `json:"tried" binding:"required,max=2000"`
	Result       string `json:"result" binding:"required,max=2000"`
	ResultSource string `json:"result_source" binding:"omitempty,oneof=user data"`
	Lesson       string `json:"lesson" binding:"required,max=2000"`
}

// CorrectInput adalah body PUT /api/memory-notes/:id; field kosong tidak diubah.
type CorrectInput struct {
	Topic        *string `json:"topic" binding:"omitempty,min=1,max=200"`
	Tried        *string `json:"tried" binding:"omitempty,min=1,max=2000"`
	Result       *string `json:"result" binding:"omitempty,min=1,max=2000"`
	ResultSource *string `json:"result_source" binding:"omitempty,oneof=user data"`
	Lesson       *string `json:"lesson" binding:"omitempty,min=1,max=2000"`
}

// ListFilter adalah parameter daftar/cari catatan.
type ListFilter struct {
	Status string // kosong = semua kecuali deleted
	Query  string // pencarian teks (kosong = urut terbaru)
	Page   int
	Limit  int
}

// Turn adalah satu pesan percakapan (teks saja) yang dipakai sebagai bahan ekstraksi.
type Turn struct {
	Role string
	Text string
}

// Hit adalah catatan yang cocok dengan sebuah pesan, untuk disisipkan ke prompt.
type Hit struct {
	MemoryNote
	Matched int     `json:"matched"` // jumlah kata pesan yang ada di catatan
	Rank    float64 `json:"rank"`    // skor ts_rank PostgreSQL
}

// MatchResult adalah respons GET /api/memory-notes/match: catatan yang akan
// disisipkan untuk pesan q, beserta blok teksnya.
type MatchResult struct {
	Query     string `json:"query"`
	Notes     []Hit  `json:"notes"`
	Block     string `json:"block"`
	TokensEst int    `json:"tokens_est"`
}

// extracted adalah satu catatan dari jawaban model ekstraksi.
type extracted struct {
	Topic        string `json:"topic"`
	Tried        string `json:"tried"`
	Result       string `json:"result"`
	ResultSource string `json:"result_source"`
	Lesson       string `json:"lesson"`
}
