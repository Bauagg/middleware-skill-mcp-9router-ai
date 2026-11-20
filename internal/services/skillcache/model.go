package skillcache

// State adalah hasil pencarian di cache.
type State string

const (
	// Hit: ada di cache.
	Hit State = "hit"
	// Miss: tidak ada di cache (Redis aktif).
	Miss State = "miss"
	// Off: cache tidak dipakai (Redis error, atau kuncinya tidak bisa dibuat).
	Off State = "off"
)

// Selection adalah hasil seleksi skill untuk sebuah pesan yang disimpan di
// cache. Keputusan pemilih cadangan (model) ikut tersimpan, jadi pesan yang
// sama tidak memanggil model lagi selama cache berlaku.
type Selection struct {
	// Dir adalah folder skill terpilih; "" berarti tidak ada skill yang cocok
	// (hasil negatif juga disimpan supaya pesan yang sama tidak dihitung ulang).
	Dir   string  `json:"d,omitempty"`
	Score float64 `json:"s,omitempty"`

	// Method adalah metode seleksi saat dihitung (bm25/embedding/fallback).
	Method string `json:"m,omitempty"`

	// Close adalah skill yang skornya berdekatan dengan skill teratas.
	Close []Scored `json:"c,omitempty"`
}

// Scored adalah folder skill beserta skornya.
type Scored struct {
	Dir   string  `json:"d"`
	Score float64 `json:"s"`
}
