package imagegen

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/database"
)

// fileNameRe membatasi nama file yang boleh disajikan, supaya request tidak
// bisa membaca file lain di server (mis. ../.env).
var fileNameRe = regexp.MustCompile(`^\d{8}-[0-9a-f]{16}\.(png|jpg|webp|gif)$`)

var extByMIME = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// Repository menyimpan file gambar di folder lokal dan catatannya di tabel
// t_generated_images.
type Repository struct {
	dir string
	db  *gorm.DB
}

// NewRepository membuat repository dan folder gambar bila belum ada.
func NewRepository(dir string, db *gorm.DB) (*Repository, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Repository{dir: dir, db: db}, nil
}

// Create mencatat satu gambar ke database.
func (r *Repository) Create(ctx context.Context, row *GeneratedImage) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// List mengambil catatan gambar terbaru lebih dulu beserta totalnya.
func (r *Repository) List(f ListFilter) ([]GeneratedImage, int64, error) {
	// query dibuat baru untuk Count dan Find, supaya state Count tidak terbawa
	base := func() *gorm.DB {
		q := database.ByUser(r.db.Model(&GeneratedImage{}), f.UserID) // "" = semua user (admin)
		if f.Query != "" {
			like := "%" + f.Query + "%"
			q = q.Where("prompt ILIKE ? OR alt ILIKE ? OR revised_prompt ILIKE ?", like, like, like)
		}
		return q
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []GeneratedImage
	err := base().Order("created_at DESC").Offset((f.Page - 1) * f.Limit).Limit(f.Limit).Find(&rows).Error
	return rows, total, err
}

// Get mengambil satu catatan gambar (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) Get(userID, id string) (*GeneratedImage, error) {
	var row GeneratedImage
	if err := database.ByUser(r.db, userID).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Save menyimpan gambar dan mengembalikan nama filenya. Nama acak, jadi tidak
// bisa ditebak dari gambar lain; diawali tanggal supaya mudah dibersihkan.
func (r *Repository) Save(data []byte, mime string) (string, error) {
	ext, ok := extByMIME[mime]
	if !ok {
		return "", fmt.Errorf("format gambar %s tidak didukung", mime)
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	name := time.Now().Format("20060102") + "-" + hex.EncodeToString(b) + ext

	// tulis ke file sementara lalu rename, supaya gambar tidak pernah
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

// Path mengembalikan lokasi file untuk disajikan; false bila nama tidak valid
// atau file tidak ada.
func (r *Repository) Path(name string) (string, bool) {
	if !fileNameRe.MatchString(name) {
		return "", false
	}
	p := filepath.Join(r.dir, name)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}
