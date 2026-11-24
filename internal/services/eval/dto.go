package eval

import "time"

// RunInput adalah body POST /api/evals/runs.
type RunInput struct {
	Skill string `json:"skill"`
	// Variants yang dibandingkan: "full", "short", "none". Kosong = full,
	// short (bila SHORT.md ada), dan none.
	Variants []string `json:"variants"`
	// Model 9Router yang diuji (wajib).
	Model string `json:"model"`
	// JudgeModel adalah model penilai; kosong = sama dengan Model.
	JudgeModel string `json:"judge_model"`
	// Limit membatasi jumlah pertanyaan yang dipakai (0 = semua).
	Limit int `json:"limit"`
	// UserID diisi controller dari header X-User-Id (bukan dari body).
	UserID string `json:"-"`
	// RouterKey diisi controller dari header X-Router-Key: eval memakai akun
	// 9Router pemanggil, bukan key milik middleware.
	RouterKey string `json:"-"`
	// RouterURL diisi controller dari header X-Router-URL (alamat 9Router pemanggil).
	RouterURL string `json:"-"`
}

// RunStarted adalah respons saat eval mulai berjalan.
type RunStarted struct {
	RunID        string   `json:"run_id"`
	Skill        string   `json:"skill"`
	SkillVersion string   `json:"skill_version"`
	Variants     []string `json:"variants"`
	Cases        int      `json:"cases"`
	Jobs         int      `json:"jobs"` // cases x variants
	Model        string   `json:"model"`
	JudgeModel   string   `json:"judge_model"`
}

// VariantSummary adalah ringkasan hasil satu varian.
type VariantSummary struct {
	Variant         string   `json:"variant"`
	Cases           int      `json:"cases"`
	Scored          int      `json:"scored"` // yang berhasil dinilai
	AvgScore        *float64 `json:"avg_score"`
	AvgSkillTokens  float64  `json:"avg_skill_tokens"`
	AvgPromptTokens *float64 `json:"avg_prompt_tokens"`
	Errors          int      `json:"errors"`
}

// RunSummary adalah hasil GET /api/evals/runs/:id.
type RunSummary struct {
	RunID        string `json:"run_id"`
	Skill        string `json:"skill"`
	SkillVersion string `json:"skill_version"`
	Model        string `json:"model"`
	JudgeModel   string `json:"judge_model"`

	// Status: "running" atau "done".
	Status    string `json:"status"`
	Total     int    `json:"total"`
	Completed int    `json:"completed"`

	Variants []VariantSummary `json:"variants"`
	// Notes adalah kesimpulan otomatis (mis. apakah SHORT.md setara SKILL.md).
	Notes []string `json:"notes"`

	// Rows hanya diisi bila detail=true.
	Rows []EvalRun `json:"rows,omitempty"`
}

// RunListItem adalah satu baris daftar eval.
type RunListItem struct {
	RunID        string    `json:"run_id"`
	UserID       string    `json:"user_id"` // yang menjalankan eval
	Skill        string    `json:"skill"`
	SkillVersion string    `json:"skill_version"`
	Model        string    `json:"model"`
	JudgeModel   string    `json:"judge_model"`
	StartedAt    time.Time `json:"started_at"`
	RowCount     int64     `json:"row_count"`
	AvgScore     *float64  `json:"avg_score"`
}

// evalFile adalah isi skills/<nama>/EVAL.yaml.
type evalFile struct {
	Cases []Case `yaml:"cases"`
}

// Case adalah satu pertanyaan uji.
type Case struct {
	Question string `yaml:"question"`
	// Criteria menjelaskan seperti apa jawaban yang baik; dibaca hakim.
	Criteria string `yaml:"criteria"`
}
