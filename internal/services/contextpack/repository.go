package contextpack

import (
	"context"

	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/database"
)

// Repository adalah akses ke tabel m_context_packs. Semua query dibatasi ke
// satu user.
type Repository struct {
	db *gorm.DB
}

// NewRepository membuat repository di atas koneksi GORM.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Migrate membuang index unik lama (nama unik global) dari sebelum ada user_id.
func (r *Repository) Migrate() error {
	return r.db.Exec(`DROP INDEX IF EXISTS idx_m_context_packs_name`).Error
}

// List mengambil context pack milik user, terurut menurut nama. userID ""
// = semua user (admin).
func (r *Repository) List(userID string) ([]ContextPack, error) {
	var rows []ContextPack
	err := database.ByUser(r.db, userID).Order("user_id, name").Find(&rows).Error
	return rows, err
}

// Active mengambil context pack aktif milik user, terurut menurut nama
// (urutan stabil membantu prefix caching di provider).
func (r *Repository) Active(ctx context.Context, userID string) ([]ContextPack, error) {
	var rows []ContextPack
	err := r.db.WithContext(ctx).Where("user_id = ? AND active = ?", userID, true).Order("name").Find(&rows).Error
	return rows, err
}

// Get mengambil satu context pack milik user (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) Get(userID, id string) (*ContextPack, error) {
	var row ContextPack
	if err := database.ByUser(r.db, userID).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Create menyimpan context pack baru.
func (r *Repository) Create(row *ContextPack) error {
	return r.db.Create(row).Error
}

// Update mengubah kolom tertentu milik user.
func (r *Repository) Update(userID, id string, fields map[string]any) error {
	return database.ByUser(r.db.Model(&ContextPack{}), userID).Where("id = ?", id).Updates(fields).Error
}

// Delete menghapus (soft delete) context pack milik user.
func (r *Repository) Delete(userID, id string) (int64, error) {
	res := database.ByUser(r.db, userID).Delete(&ContextPack{}, "id = ?", id)
	return res.RowsAffected, res.Error
}
