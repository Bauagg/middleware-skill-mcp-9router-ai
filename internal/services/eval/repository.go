package eval

import (
	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/database"
)

// Repository adalah akses ke tabel t_eval_runs.
type Repository struct {
	db *gorm.DB
}

// NewRepository membuat repository di atas koneksi GORM.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Save menyimpan satu hasil (disimpan per jawaban supaya kemajuan eval terlihat).
func (r *Repository) Save(row *EvalRun) error {
	return r.db.Create(row).Error
}

// ByRun mengambil semua hasil satu eval, terurut per varian dan pertanyaan.
func (r *Repository) ByRun(userID, runID string) ([]EvalRun, error) {
	var rows []EvalRun
	err := database.ByUser(r.db, userID).Where("run_id = ?", runID).Order("variant, case_index").Find(&rows).Error
	return rows, err
}

// Runs mengambil daftar eval terbaru, opsional untuk satu skill.
func (r *Repository) Runs(userID, skill string, limit int) ([]RunListItem, error) {
	q := database.ByUser(r.db.Model(&EvalRun{}), userID).
		Select(`run_id, user_id, skill, skill_version, model, judge_model,
			MIN(created_at) AS started_at,
			COUNT(*) AS row_count,
			ROUND(AVG(score)::numeric, 2)::float8 AS avg_score`).
		Group("run_id, user_id, skill, skill_version, model, judge_model").
		Order("started_at DESC").
		Limit(limit)
	if skill != "" {
		q = q.Where("skill = ?", skill)
	}

	var rows []RunListItem
	err := q.Scan(&rows).Error
	if rows == nil {
		rows = []RunListItem{}
	}
	return rows, err
}
