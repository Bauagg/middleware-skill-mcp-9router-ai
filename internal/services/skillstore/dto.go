package skillstore

import "time"

// Varian teks skill hasil Render.
const (
	VariantFull      = "full"      // SKILL.md utuh (muat dalam budget)
	VariantShort     = "short"     // diganti SHORT.md karena SKILL.md melebihi budget
	VariantTruncated = "truncated" // dipotong karena tetap melebihi budget
)

// Rendered adalah teks skill yang siap disisipkan ke prompt.
type Rendered struct {
	Text          string `json:"text"`
	Tokens        int    `json:"tokens"` // perkiraan (karakter / 4)
	Variant       string `json:"variant"`
	RemovedLines  int    `json:"removed_lines"`  // baris khusus Claude yang dibuang
	RemovedBlocks int    `json:"removed_blocks"` // blok kode script yang dibuang
}

// PreviewResult adalah hasil GET /api/skills/preview/:name.
type PreviewResult struct {
	Name   string `json:"name"`
	Budget int    `json:"budget"` // 0 = tanpa batas
	Rendered
}

// SkillDetail adalah satu skill yang terpasang di folder skill (hasil GET).
type SkillDetail struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Files       int       `json:"files"`
	HasShort    bool      `json:"has_short"`
	SizeBytes   int64     `json:"size_bytes"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Error terisi bila folder ada tetapi SKILL.md-nya tidak bisa dipakai
	// (hilang atau frontmatter rusak), supaya skill bermasalah tetap kelihatan.
	Error string `json:"error,omitempty"`
}

// UploadInput adalah parameter selain file zip pada endpoint upload.
type UploadInput struct {
	// Overwrite true = skill dengan nama yang sama akan diganti.
	Overwrite bool
}

// SkillInfo menggambarkan satu skill yang berhasil dipasang.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Files       int    `json:"files"`
	HasShort    bool   `json:"has_short"`
	Replaced    bool   `json:"replaced"`
}

// UploadResult adalah hasil upload zip.
type UploadResult struct {
	Skills []SkillInfo `json:"skills"`
	// SkippedFiles berisi file yang tidak dipasang (ekstensi tidak didukung
	// atau berada di luar folder skill), supaya user tahu apa yang dibuang.
	SkippedFiles []string `json:"skipped_files"`
}
