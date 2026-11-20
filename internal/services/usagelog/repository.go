package usagelog

import (
	"fmt"

	"gorm.io/gorm"
)

// Repository adalah akses ke tabel t_usage_logs.
type Repository struct {
	db *gorm.DB
}

// NewRepository membuat repository di atas koneksi GORM.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// CreateBatch menyimpan banyak baris sekaligus.
func (r *Repository) CreateBatch(logs []UsageLog) error {
	return r.db.CreateInBatches(logs, 100).Error
}

// List mengambil log terbaru lebih dulu, beserta total baris yang cocok filter.
func (r *Repository) List(f ListFilter) ([]UsageLog, int64, error) {
	q := r.db.Model(&UsageLog{})
	if f.Model != "" {
		q = q.Where("model = ?", f.Model)
	}
	if f.Skill != "" {
		q = q.Where("skill_name = ?", f.Skill)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []UsageLog
	err := q.Order("created_at DESC").Offset((f.Page - 1) * f.Limit).Limit(f.Limit).Find(&rows).Error
	return rows, total, err
}

// Summary meringkas pemakaian per skill, status cache, varian skill, dan
// penyisipan memori pada rentang waktu tertentu.
func (r *Repository) Summary(f SummaryFilter) (SummaryResult, error) {
	res := SummaryResult{}

	if err := r.inRange(r.db.Model(&UsageLog{}), f).
		Select(`COALESCE(skill_name, '') AS skill,
			COUNT(*) AS requests,
			COUNT(prompt_tokens) AS with_usage,
			AVG(extra_tokens_est)::float8 AS avg_extra_tokens_est,
			AVG(prompt_tokens)::float8 AS avg_prompt_tokens,
			AVG(completion_tokens)::float8 AS avg_completion_tokens,
			AVG(cached_tokens)::float8 AS avg_cached_tokens,
			AVG(latency_ms)::float8 AS avg_latency_ms`).
		Group("COALESCE(skill_name, '')").
		Order("requests DESC").
		Scan(&res.Skills).Error; err != nil {
		return res, err
	}
	if res.Skills == nil {
		res.Skills = []SummaryRow{}
	}

	var err error
	if res.SelectionCache, err = r.countBy("selection_cache", f); err != nil {
		return res, err
	}
	if res.SessionCache, err = r.countBy("session_cache", f); err != nil {
		return res, err
	}
	if res.Variants, err = r.countBy("skill_variant", f); err != nil {
		return res, err
	}
	if res.Methods, err = r.countBy("selection_method", f); err != nil {
		return res, err
	}

	err = r.inRange(r.db.Model(&UsageLog{}), f).
		Select(`COUNT(*) FILTER (WHERE context_pack_tokens_est > 0) AS with_context_pack,
			COUNT(*) FILTER (WHERE memory_notes > 0) AS with_memory_notes,
			COALESCE(AVG(context_pack_tokens_est) FILTER (WHERE context_pack_tokens_est > 0), 0)::float8 AS avg_context_pack_tokens_est,
			COALESCE(AVG(memory_tokens_est) FILTER (WHERE memory_notes > 0), 0)::float8 AS avg_memory_tokens_est,
			COALESCE(AVG(memory_notes) FILTER (WHERE memory_notes > 0), 0)::float8 AS avg_memory_notes`).
		Scan(&res.Memory).Error
	if err != nil {
		return res, err
	}

	err = r.inRange(r.db.Model(&UsageLog{}), f).
		Select(`COUNT(*) FILTER (WHERE mcp_tools_offered > 0) AS with_tools_offered,
			COUNT(*) FILTER (WHERE mcp_tool_calls > 0) AS with_tool_calls,
			COALESCE(AVG(mcp_tools_tokens_est) FILTER (WHERE mcp_tools_offered > 0), 0)::float8 AS avg_tools_tokens_est,
			COALESCE(AVG(mcp_tool_calls) FILTER (WHERE mcp_tool_calls > 0), 0)::float8 AS avg_tool_calls,
			COALESCE(AVG(mcp_steps) FILTER (WHERE mcp_tool_calls > 0), 0)::float8 AS avg_steps_with_calls`).
		Scan(&res.MCP).Error
	return res, err
}

// countBy menghitung baris per nilai sebuah kolom. Hanya kolom di daftar
// berikut yang diizinkan, karena nama kolom disusun langsung ke SQL.
// Nilai kosong/NULL dilaporkan sebagai "none".
func (r *Repository) countBy(column string, f SummaryFilter) (map[string]int64, error) {
	switch column {
	case "selection_cache", "session_cache", "skill_variant", "selection_method":
	default:
		return nil, fmt.Errorf("kolom %q tidak diizinkan", column)
	}

	var rows []struct {
		Label string
		Total int64
	}
	err := r.inRange(r.db.Model(&UsageLog{}), f).
		Select("COALESCE(NULLIF(" + column + ", ''), 'none') AS label, COUNT(*) AS total").
		Group("COALESCE(NULLIF(" + column + ", ''), 'none')").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.Label] = row.Total
	}
	return out, nil
}

// inRange menerapkan batas waktu ringkasan.
func (r *Repository) inRange(q *gorm.DB, f SummaryFilter) *gorm.DB {
	if f.From != nil {
		q = q.Where("created_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("created_at < ?", *f.To)
	}
	return q
}
