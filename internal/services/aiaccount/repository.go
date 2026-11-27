package aiaccount

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/database"
)

// videoNameRe membatasi nama file yang boleh disajikan, supaya request tidak
// bisa membaca file lain di server (mis. ../.env).
var videoNameRe = regexp.MustCompile(`^\d{8}-[0-9a-f]{16}\.mp4$`)

// Repository menyimpan provider, akun, dan catatan video di database, serta
// file video di folder lokal.
type Repository struct {
	db  *gorm.DB
	dir string
}

// NewRepository membuat repository dan folder video bila belum ada.
func NewRepository(db *gorm.DB, videoDir string) (*Repository, error) {
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		return nil, err
	}
	return &Repository{db: db, dir: videoDir}, nil
}

// Migrate membuang tabel m_gemini_accounts dari sebelum akun mendukung banyak
// provider, bila masih kosong (bila berisi, dibiarkan supaya tidak ada data hilang).
func (r *Repository) Migrate() error {
	const old = "m_gemini_accounts"
	if !r.db.Migrator().HasTable(old) {
		return nil
	}
	var n int64
	if err := r.db.Table(old).Count(&n).Error; err != nil || n > 0 {
		return err
	}
	return r.db.Migrator().DropTable(old)
}

// --- provider ---

// Providers mengambil semua provider, urut prioritas.
func (r *Repository) Providers(ctx context.Context) ([]Provider, error) {
	var rows []Provider
	err := r.db.WithContext(ctx).Order("priority ASC, created_at ASC").Find(&rows).Error
	return rows, err
}

// Provider mengambil satu provider (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) Provider(ctx context.Context, id string) (*Provider, error) {
	var row Provider
	if err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateProvider menyimpan provider baru.
func (r *Repository) CreateProvider(ctx context.Context, row *Provider) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// UpdateProvider mengubah kolom provider.
func (r *Repository) UpdateProvider(ctx context.Context, id string, fields map[string]any) error {
	res := r.db.WithContext(ctx).Model(&Provider{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteProvider menghapus provider beserta semua akun di dalamnya.
func (r *Repository) DeleteProvider(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&Account{}, "provider_id = ?", id).Error; err != nil {
			return err
		}
		res := tx.Delete(&Provider{}, "id = ?", id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// --- akun ---

// Accounts mengambil akun milik user, urut prioritas di dalam provider.
// userID "" = semua user (admin, dan perawatan sesi berkala).
func (r *Repository) Accounts(ctx context.Context, userID string) ([]Account, error) {
	var rows []Account
	err := database.ByUser(r.db.WithContext(ctx), userID).Order("user_id, provider_id, priority ASC, created_at ASC").Find(&rows).Error
	return rows, err
}

// Account mengambil satu akun milik user; userID "" = akun siapa pun
// (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) Account(ctx context.Context, userID, id string) (*Account, error) {
	var row Account
	if err := database.ByUser(r.db.WithContext(ctx), userID).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateAccount menyimpan akun baru.
func (r *Repository) CreateAccount(ctx context.Context, row *Account) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// UpdateAccount mengubah kolom akun. Map dipakai supaya nilai kosong/false
// (mis. enabled=false) ikut tersimpan.
func (r *Repository) UpdateAccount(ctx context.Context, id string, fields map[string]any) error {
	res := r.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// AddUsage menambah hitungan pemakaian akun tanpa menimpa perubahan lain.
func (r *Repository) AddUsage(ctx context.Context, id, column string) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).
		Updates(map[string]any{column: gorm.Expr(column + " + 1"), "last_used_at": now}).Error
}

// DeleteAccount menghapus akun.
func (r *Repository) DeleteAccount(ctx context.Context, id string) error {
	res := r.db.WithContext(ctx).Delete(&Account{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// --- video ---

// CreateVideo mencatat satu video ke database.
func (r *Repository) CreateVideo(ctx context.Context, row *GeneratedVideo) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// Videos mengambil catatan video terbaru lebih dulu beserta totalnya.
func (r *Repository) Videos(f VideoFilter) ([]GeneratedVideo, int64, error) {
	base := func() *gorm.DB {
		q := database.ByUser(r.db.Model(&GeneratedVideo{}), f.UserID) // "" = semua user (admin)
		if f.Query != "" {
			q = q.Where("prompt ILIKE ?", "%"+f.Query+"%")
		}
		return q
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []GeneratedVideo
	err := base().Order("created_at DESC").Offset((f.Page - 1) * f.Limit).Limit(f.Limit).Find(&rows).Error
	return rows, total, err
}

// Video mengambil satu catatan video (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) Video(userID, id string) (*GeneratedVideo, error) {
	var row GeneratedVideo
	if err := database.ByUser(r.db, userID).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// SaveVideo menyimpan file video dan mengembalikan namanya (acak, diawali tanggal).
func (r *Repository) SaveVideo(data []byte) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	name := time.Now().Format("20060102") + "-" + hex.EncodeToString(b) + ".mp4"

	// tulis ke file sementara lalu rename, supaya video tidak pernah
	// tersaji setengah jadi
	tmp := filepath.Join(r.dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(r.dir, name)); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return name, nil
}

// VideoPath mengembalikan lokasi file untuk disajikan; false bila nama tidak
// valid atau file tidak ada.
func (r *Repository) VideoPath(name string) (string, bool) {
	if !videoNameRe.MatchString(name) {
		return "", false
	}
	p := filepath.Join(r.dir, name)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}
