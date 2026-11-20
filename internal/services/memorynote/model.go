package memorynote

import "time"

// Status catatan. Hanya active dan corrected yang disisipkan ke prompt.
const (
	StatusProposed  = "proposed"  // hasil ekstraksi, menunggu konfirmasi lewat API
	StatusActive    = "active"    // dikonfirmasi (atau ditulis manual)
	StatusCorrected = "corrected" // isinya diperbaiki lewat API; tetap dipakai
	StatusRejected  = "rejected"  // ditolak; tidak diusulkan lagi
	StatusDeleted   = "deleted"   // dihapus; barisnya disimpan supaya tidak diusulkan lagi
)

// Sumber hasil yang dicatat.
const (
	SourceUser = "user" // klaim user tanpa angka
	SourceData = "data" // disertai angka/data terukur
)

// Asal catatan.
const (
	OriginExtract = "extract" // diekstrak otomatis dari percakapan
	OriginManual  = "manual"  // ditulis lewat API
)

// MemoryNote adalah satu baris tabel t_memory_notes: satu pengalaman yang
// pernah dicoba beserta hasilnya. Memori ini global (tidak terikat user).
//
// Kolom search_vector (tsvector, untuk pencarian teks PostgreSQL) sengaja tidak
// ada di struct: kolom itu dihitung database sendiri (generated column) dan
// dibuat oleh Repository.Migrate.
type MemoryNote struct {
	ID string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`

	Topic        string `json:"topic" gorm:"size:200;not null"`
	Tried        string `json:"tried" gorm:"type:text;not null"`
	Result       string `json:"result" gorm:"type:text;not null"`
	ResultSource string `json:"result_source" gorm:"size:10;not null"`
	Lesson       string `json:"lesson" gorm:"type:text;not null"`

	Status string `json:"status" gorm:"size:20;not null;index"`
	Origin string `json:"origin" gorm:"size:10;not null"`

	// SourceExcerpt adalah potongan pesan user asal catatan (kosong bila manual).
	// SourceHash adalah sha256 pesan itu, supaya pesan yang sama (mis. request
	// diulang client) tidak diekstrak dua kali.
	SourceExcerpt string `json:"source_excerpt" gorm:"type:text"`
	SourceHash    string `json:"-" gorm:"size:64;index"`

	// NoteHash adalah sha256 isi catatan (topik, yang dicoba, hasil). Unik,
	// supaya catatan yang sama tidak tersimpan dua kali, termasuk yang sudah
	// ditolak atau dihapus (tidak diusulkan lagi).
	NoteHash string `json:"-" gorm:"size:64;not null;uniqueIndex"`

	// ExtractModel adalah model yang mengekstrak catatan (kosong bila manual).
	ExtractModel string `json:"extract_model" gorm:"size:200"`

	// ExpiresAt: setelah lewat, catatan tidak disisipkan lagi (NULL = tanpa batas).
	// Diisi saat catatan dikonfirmasi/dikoreksi, karena hasil bisnis bisa basi.
	ExpiresAt   *time.Time `json:"expires_at" gorm:"index"`
	ConfirmedAt *time.Time `json:"confirmed_at"`

	CreatedAt time.Time `json:"created_at" gorm:"index"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName menetapkan nama tabel. Tabel transaksi diberi awalan "t_".
func (MemoryNote) TableName() string { return "t_memory_notes" }
