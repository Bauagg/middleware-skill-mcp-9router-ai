package contextpack

import (
	"time"

	"gorm.io/gorm"
)

// ContextPack adalah satu baris tabel m_context_packs: ringkasan tetap yang
// selalu relevan (gambaran bisnis, istilah, aturan tetap). Semua context pack
// yang aktif disisipkan ke prompt setelah skill.
type ContextPack struct {
	ID      string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name    string `json:"name" gorm:"size:100;not null;uniqueIndex:idx_m_context_packs_name,where:deleted_at IS NULL"`
	Content string `json:"content" gorm:"type:text;not null"`
	// Active false = disimpan tapi tidak disisipkan ke prompt.
	Active bool `json:"active" gorm:"not null"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName menetapkan nama tabel. Data master diberi awalan "m_".
func (ContextPack) TableName() string { return "m_context_packs" }
