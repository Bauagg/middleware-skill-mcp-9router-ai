package eval

import "time"

// EvalRun adalah satu baris tabel t_eval_runs: satu pertanyaan uji yang
// dijawab model dengan satu varian skill, lalu dinilai hakim. Satu kali eval
// (run_id yang sama) berisi pertanyaan x varian baris.
type EvalRun struct {
	ID    string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	RunID string `json:"run_id" gorm:"type:uuid;index"`

	// UserID yang menjalankan eval (header X-User-Id).
	UserID string `json:"user_id" gorm:"size:128;not null;default:'';index"`

	Skill string `json:"skill" gorm:"size:200;index"`
	// SkillVersion adalah hash isi SKILL.md + SHORT.md saat eval, supaya hasil
	// sebelum dan sesudah skill diedit bisa dibandingkan.
	SkillVersion string `json:"skill_version" gorm:"size:20"`
	// Variant: "full" (SKILL.md), "short" (SHORT.md), atau "none" (tanpa skill, pembanding).
	Variant string `json:"variant" gorm:"size:20"`

	Model      string `json:"model" gorm:"size:200"`
	JudgeModel string `json:"judge_model" gorm:"size:200"`

	CaseIndex int    `json:"case_index"`
	Question  string `json:"question" gorm:"type:text"`
	Criteria  string `json:"criteria" gorm:"type:text"`
	Answer    string `json:"answer" gorm:"type:text"`

	// Score 0-10 dari hakim; NULL bila jawaban atau penilaian gagal (lihat Error).
	Score  *float64 `json:"score"`
	Reason string   `json:"reason" gorm:"type:text"`

	SkillTokensEst   int   `json:"skill_tokens_est"` // perkiraan token skill yang disisipkan (0 untuk "none")
	PromptTokens     *int  `json:"prompt_tokens"`
	CompletionTokens *int  `json:"completion_tokens"`
	JudgeTokens      int   `json:"judge_tokens"`
	LatencyMs        int64 `json:"latency_ms"` // waktu model menjawab (tanpa penilaian)

	Error string `json:"error,omitempty" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at" gorm:"index"`
}

// TableName menetapkan nama tabel. Tabel transaksi diberi awalan "t_".
func (EvalRun) TableName() string { return "t_eval_runs" }
