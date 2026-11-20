package contextpack

import (
	"context"

	"gorm.io/gorm"
)

// Repository adalah akses ke tabel m_context_packs.
type Repository struct {
	db *gorm.DB
}

// NewRepository membuat repository di atas koneksi GORM.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// List mengambil semua context pack, terurut menurut nama.
func (r *Repository) List() ([]ContextPack, error) {
	var rows []ContextPack
	err := r.db.Order("name").Find(&rows).Error
	return rows, err
}

// Active mengambil context pack yang aktif, terurut menurut nama (urutan yang
// stabil membantu prefix caching di provider).
func (r *Repository) Active(ctx context.Context) ([]ContextPack, error) {
	var rows []ContextPack
	err := r.db.WithContext(ctx).Where("active = ?", true).Order("name").Find(&rows).Error
	return rows, err
}

// Get mengambil satu context pack (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) Get(id string) (*ContextPack, error) {
	var row ContextPack
	if err := r.db.First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Create menyimpan context pack baru.
func (r *Repository) Create(row *ContextPack) error {
	return r.db.Create(row).Error
}

// Update mengubah kolom tertentu.
func (r *Repository) Update(id string, fields map[string]any) error {
	return r.db.Model(&ContextPack{}).Where("id = ?", id).Updates(fields).Error
}

// Delete menghapus (soft delete) context pack.
func (r *Repository) Delete(id string) (int64, error) {
	res := r.db.Delete(&ContextPack{}, "id = ?", id)
	return res.RowsAffected, res.Error
}
