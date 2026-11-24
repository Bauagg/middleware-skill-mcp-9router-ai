package auth

import (
	"context"

	"gorm.io/gorm"
)

// Repository mengakses tabel m_users.
type Repository struct {
	db *gorm.DB
}

// NewRepository membuat repository.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Create menyimpan user baru (email ganda = error unique violation).
func (r *Repository) Create(ctx context.Context, row *User) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// ByEmail mengambil user berdasarkan email (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) ByEmail(ctx context.Context, email string) (*User, error) {
	var row User
	if err := r.db.WithContext(ctx).First(&row, "email = ?", email).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ByID mengambil user berdasarkan id (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) ByID(ctx context.Context, id string) (*User, error) {
	var row User
	if err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// SetRole mengubah role user.
func (r *Repository) SetRole(ctx context.Context, id, role string) error {
	return r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("role", role).Error
}
