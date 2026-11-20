package skillcache

import (
	"context"
	"encoding/json"
	"time"
)

// KV adalah penyimpanan key-value dengan masa berlaku; clients.Redis
// memenuhinya. Didefinisikan di sini sebagai kebutuhan Repository.
type KV interface {
	Get(ctx context.Context, key string) (value string, found bool, err error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}

// Repository adalah akses ke cache: menentukan bentuk key dan cara menyimpan
// nilai. kv nil = cache dimatikan.
type Repository struct {
	kv KV
}

// NewRepository membuat repository di atas kv (boleh nil).
func NewRepository(kv KV) *Repository {
	return &Repository{kv: kv}
}

// Enabled melaporkan apakah cache aktif.
func (r *Repository) Enabled() bool { return r.kv != nil }

// Key diberi versi index (selection) supaya cache otomatis "kosong" setiap
// index dibangun ulang: key lama tidak pernah dibaca lagi dan hilang sendiri
// oleh TTL, tanpa perlu menghapus satu per satu.
func selectionKey(version, queryKey string) string {
	return "skillmw:sel:" + version + ":" + queryKey
}

func sessionKey(conversationKey string) string {
	return "skillmw:sess:" + conversationKey
}

// GetSelection membaca hasil seleksi yang tersimpan (JSON).
func (r *Repository) GetSelection(ctx context.Context, version, queryKey string) (Selection, State) {
	v, found, err := r.kv.Get(ctx, selectionKey(version, queryKey))
	if err != nil {
		return Selection{}, Off
	}
	if !found {
		return Selection{}, Miss
	}

	var sel Selection
	if json.Unmarshal([]byte(v), &sel) != nil {
		return Selection{}, Miss // nilai rusak atau format lama dianggap tidak ada
	}
	return sel, Hit
}

// SetSelection menyimpan hasil seleksi sebagai JSON ringkas.
func (r *Repository) SetSelection(ctx context.Context, version, queryKey string, sel Selection, ttl time.Duration) {
	data, err := json.Marshal(sel)
	if err != nil {
		return
	}
	_ = r.kv.Set(ctx, selectionKey(version, queryKey), string(data), ttl) // error sudah dicatat klien; cache hanya optimasi
}

// GetSession membaca folder skill aktif sebuah percakapan.
func (r *Repository) GetSession(ctx context.Context, conversationKey string) (string, State) {
	v, found, err := r.kv.Get(ctx, sessionKey(conversationKey))
	if err != nil {
		return "", Off
	}
	if !found || v == "" {
		return "", Miss
	}
	return v, Hit
}

// SetSession menyimpan folder skill aktif sebuah percakapan.
func (r *Repository) SetSession(ctx context.Context, conversationKey, dir string, ttl time.Duration) {
	_ = r.kv.Set(ctx, sessionKey(conversationKey), dir, ttl)
}
