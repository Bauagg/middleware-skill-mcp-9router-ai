package skillindex

import "sync/atomic"

// Repository menyimpan snapshot index aktif di memori. Snapshot baru dibangun
// terpisah lalu ditukar dalam satu langkah atomik, sehingga request yang
// sedang berjalan tidak pernah melihat index setengah jadi.
type Repository struct {
	cur atomic.Pointer[Snapshot]
}

// NewRepository membuat repository dengan snapshot kosong.
func NewRepository() *Repository {
	r := &Repository{}
	r.cur.Store(&Snapshot{})
	return r
}

// Load mengembalikan snapshot aktif (tidak pernah nil).
func (r *Repository) Load() *Snapshot {
	return r.cur.Load()
}

// Replace mengganti snapshot aktif.
func (r *Repository) Replace(s *Snapshot) {
	r.cur.Store(s)
}
