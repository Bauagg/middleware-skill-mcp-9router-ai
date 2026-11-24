package mcpinstall

import (
	"context"

	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/database"
)

// Repository mengakses tabel m_mcp_installs.
type Repository struct {
	db *gorm.DB
}

// NewRepository membuat repository.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create menyimpan pemasangan baru (nama ganda = error unique violation).
func (r *Repository) Create(ctx context.Context, row *Install) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// Get mengambil satu pemasangan. userID "" = milik siapa pun (admin).
func (r *Repository) Get(ctx context.Context, userID, id string) (*Install, error) {
	var row Install
	if err := database.ByUser(r.db.WithContext(ctx), userID).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// GetByName mengambil pemasangan berdasarkan nama. userID "" = milik siapa pun.
func (r *Repository) GetByName(ctx context.Context, userID, name string) (*Install, error) {
	var row Install
	if err := database.ByUser(r.db.WithContext(ctx), userID).First(&row, "name = ?", name).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// List mengambil pemasangan terbaru lebih dulu: milik userID ditambah
// pemasangan admin untuk semua user. userID "" = semua (admin).
func (r *Repository) List(ctx context.Context, userID, status string) ([]Install, error) {
	q := r.db.WithContext(ctx)
	if userID != "" {
		q = q.Where("user_id = ? OR global = ?", userID, true)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []Install
	err := q.Order("created_at DESC").Limit(200).Find(&rows).Error
	return rows, err
}

// ByStatus mengambil semua pemasangan dengan salah satu status.
func (r *Repository) ByStatus(ctx context.Context, statuses ...string) ([]Install, error) {
	var rows []Install
	err := r.db.WithContext(ctx).Where("status IN ?", statuses).Find(&rows).Error
	return rows, err
}

// Update mengubah kolom tertentu.
func (r *Repository) Update(ctx context.Context, id string, fields map[string]any) error {
	return r.db.WithContext(ctx).Model(&Install{}).Where("id = ?", id).Updates(fields).Error
}

// GetVisible mengambil pemasangan yang boleh dilihat userID: miliknya atau
// buatan admin untuk semua user. userID "" = semua (admin). Cari menurut id
// atau nama.
func (r *Repository) GetVisible(ctx context.Context, userID, column, value string) (*Install, error) {
	q := r.db.WithContext(ctx).Where(column+" = ?", value)
	if userID != "" {
		q = q.Where("user_id = ? OR global = ?", userID, true)
	}
	var row Install
	if err := q.First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// GetInstance mengambil instance milik user untuk satu template (gorm.ErrRecordNotFound bila belum ada).
func (r *Repository) GetInstance(ctx context.Context, installID, userID string) (*Instance, error) {
	var row Instance
	if err := r.db.WithContext(ctx).First(&row, "install_id = ? AND user_id = ?", installID, userID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateInstance menyimpan instance baru.
func (r *Repository) CreateInstance(ctx context.Context, row *Instance) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// UpdateInstance mengubah kolom tertentu instance.
func (r *Repository) UpdateInstance(ctx context.Context, id string, fields map[string]any) error {
	return r.db.WithContext(ctx).Model(&Instance{}).Where("id = ?", id).Updates(fields).Error
}

// Instances mengambil semua instance satu template.
func (r *Repository) Instances(ctx context.Context, installID string) ([]Instance, error) {
	var rows []Instance
	err := r.db.WithContext(ctx).Where("install_id = ?", installID).Find(&rows).Error
	return rows, err
}

// InstancesByStatus mengambil instance dengan salah satu status.
func (r *Repository) InstancesByStatus(ctx context.Context, statuses ...string) ([]Instance, error) {
	var rows []Instance
	err := r.db.WithContext(ctx).Where("status IN ?", statuses).Find(&rows).Error
	return rows, err
}

// DeleteInstance menghapus satu instance.
func (r *Repository) DeleteInstance(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&Instance{}, "id = ?", id).Error
}

// Delete menghapus pemasangan.
func (r *Repository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&Instance{}, "install_id = ?", id).Error; err != nil {
			return err
		}
		return tx.Delete(&Install{}, "id = ?", id).Error
	})
}
