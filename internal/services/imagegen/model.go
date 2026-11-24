package imagegen

import "time"

// GeneratedImage adalah satu baris tabel t_generated_images: satu gambar yang
// dibuat lewat chat (tool generate_image). File gambarnya ada di IMAGE_PATH.
type GeneratedImage struct {
	ID string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`

	// UserID pembuat gambar (header X-User-Id).
	UserID string `json:"user_id" gorm:"size:128;not null;default:'';index"`

	// URL lengkap saat gambar dibuat (BASE_URL/images/<file>) dan nama filenya.
	// Bila BASE_URL berubah, file tetap bisa dibuka di alamat baru dengan nama
	// yang sama.
	URL      string `json:"url" gorm:"type:text;not null"`
	FileName string `json:"file_name" gorm:"size:64;not null;uniqueIndex"`

	// Prompt yang ditulis model, prompt yang dipakai provider (bila
	// dilaporkan), dan keterangan singkat gambar dalam bahasa user.
	Prompt        string `json:"prompt" gorm:"type:text;not null"`
	RevisedPrompt string `json:"revised_prompt" gorm:"type:text"`
	Alt           string `json:"alt" gorm:"size:500"`

	Model     string `json:"model" gorm:"size:200;index"`
	MIME      string `json:"mime" gorm:"size:50"`
	SizeBytes int    `json:"size_bytes"`
	LatencyMs int64  `json:"latency_ms"`

	CreatedAt time.Time `json:"created_at" gorm:"index"`
}

// TableName menetapkan nama tabel. Tabel transaksi diberi awalan "t_".
func (GeneratedImage) TableName() string { return "t_generated_images" }
