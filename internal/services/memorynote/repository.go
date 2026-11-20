package memorynote

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// searchSchema membuat kolom pencarian teks dan index-nya. Kolomnya generated
// (dihitung PostgreSQL dari isi catatan), jadi selalu sinkron tanpa trigger.
// Konfigurasi 'simple' dipakai karena PostgreSQL tidak punya kamus bahasa
// Indonesia; akhiran kata ditangani lewat pencarian prefix (lihat tsQuery).
var searchSchema = []string{
	`ALTER TABLE t_memory_notes ADD COLUMN IF NOT EXISTS search_vector tsvector
		GENERATED ALWAYS AS (to_tsvector('simple',
			coalesce(topic, '') || ' ' || coalesce(tried, '') || ' ' ||
			coalesce(result, '') || ' ' || coalesce(lesson, ''))) STORED`,
	`CREATE INDEX IF NOT EXISTS idx_t_memory_notes_search ON t_memory_notes USING GIN (search_vector)`,
}

// usableStatuses adalah status catatan yang boleh disisipkan ke prompt.
var usableStatuses = []string{StatusActive, StatusCorrected}

// Repository adalah akses ke tabel t_memory_notes.
type Repository struct {
	db *gorm.DB
}

// NewRepository membuat repository di atas koneksi GORM.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Migrate menambahkan kolom dan index pencarian teks. Dipanggil setelah
// AutoMigrate membuat tabelnya.
func (r *Repository) Migrate() error {
	for _, sql := range searchSchema {
		if err := r.db.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}

// List mengambil catatan sesuai filter beserta total barisnya. Bila tsq diisi,
// hanya catatan yang cocok yang diambil, diurutkan dari yang paling relevan.
func (r *Repository) List(f ListFilter, tsq string) ([]MemoryNote, int64, error) {
	base := func() *gorm.DB {
		q := r.db.Model(&MemoryNote{})
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
		} else {
			q = q.Where("status <> ?", StatusDeleted)
		}
		if tsq != "" {
			q = q.Where("search_vector @@ to_tsquery('simple', ?)", tsq)
		}
		return q
	}

	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	q := base()
	if tsq != "" {
		q = q.Order(clause.OrderBy{Expression: clause.Expr{
			SQL:                "ts_rank(search_vector, to_tsquery('simple', ?)) DESC, created_at DESC",
			Vars:               []any{tsq},
			WithoutParentheses: true,
		}})
	} else {
		q = q.Order("created_at DESC")
	}

	var rows []MemoryNote
	err := q.Offset((f.Page - 1) * f.Limit).Limit(f.Limit).Find(&rows).Error
	return rows, total, err
}

// Search mengambil catatan yang boleh dipakai (aktif/dikoreksi, belum
// kedaluwarsa) dan cocok dengan tsq, paling relevan lebih dulu.
func (r *Repository) Search(ctx context.Context, tsq string, limit int) ([]Hit, error) {
	var hits []Hit
	err := r.db.WithContext(ctx).Raw(`
		SELECT n.*, ts_rank(n.search_vector, q) AS rank
		FROM t_memory_notes n, to_tsquery('simple', ?) q
		WHERE n.search_vector @@ q
		  AND n.status IN ?
		  AND (n.expires_at IS NULL OR n.expires_at > ?)
		ORDER BY rank DESC, n.created_at DESC
		LIMIT ?`, tsq, usableStatuses, time.Now(), limit).
		Scan(&hits).Error
	return hits, err
}

// Get mengambil satu catatan (gorm.ErrRecordNotFound bila tidak ada).
func (r *Repository) Get(id string) (*MemoryNote, error) {
	var row MemoryNote
	if err := r.db.First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Create menyimpan satu catatan.
func (r *Repository) Create(row *MemoryNote) error {
	return r.db.Create(row).Error
}

// CreateIgnore menyimpan catatan hasil ekstraksi; catatan yang isinya sudah
// pernah ada (note_hash sama, termasuk yang ditolak/dihapus) dilewati.
// Mengembalikan jumlah catatan yang benar-benar tersimpan.
func (r *Repository) CreateIgnore(ctx context.Context, rows []MemoryNote) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	res := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "note_hash"}}, DoNothing: true}).
		Create(&rows)
	return res.RowsAffected, res.Error
}

// SourceSeen melaporkan apakah pesan dengan hash ini sudah pernah diekstrak.
func (r *Repository) SourceSeen(ctx context.Context, hash string) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&MemoryNote{}).Where("source_hash = ?", hash).Limit(1).Count(&n).Error
	return n > 0, err
}

// Update mengubah kolom tertentu.
func (r *Repository) Update(id string, fields map[string]any) error {
	return r.db.Model(&MemoryNote{}).Where("id = ?", id).Updates(fields).Error
}

// tsQuery menyusun query to_tsquery dari kata-kata pesan: kata digabung dengan
// OR dan dicari sebagai prefix ("hasil:*" juga cocok dengan "hasilnya"), karena
// tokenizer kita memotong akhiran sedangkan tsvector 'simple' tidak. Kata hanya
// berisi huruf/angka (hasil bm25.Tokenize), jadi aman dari sintaks tsquery.
func tsQuery(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		parts = append(parts, t+":*")
	}
	return strings.Join(parts, " | ")
}
