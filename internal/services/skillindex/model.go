package skillindex

import (
	"time"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/bm25"
)

// IndexEntry adalah satu skill di dalam index (hanya metadata untuk pencarian;
// isi SKILL.md dibaca dari disk saat skill terpilih).
type IndexEntry struct {
	Dir         string   // nama folder di disk, dipakai untuk membaca isi skill
	Name        string   // dari frontmatter (cadangan: nama folder)
	Description string   // dari frontmatter, menjelaskan kapan skill dipakai
	Keywords    []string // dari frontmatter (opsional), tambahan kata kunci pencarian
	HasShort    bool

	// Vector adalah embedding nama+deskripsi+keywords; nil bila embedding
	// dimatikan atau gagal dihitung.
	Vector []float32
}

// Snapshot adalah isi index pada satu waktu. Tidak pernah diubah setelah
// dibuat, jadi aman dibaca banyak request tanpa lock.
type Snapshot struct {
	Entries  []IndexEntry
	LoadedAt time.Time

	corpus *bm25.Corpus // statistik BM25, dibangun bersama snapshot

	// allVectors true bila semua skill punya embedding. Seleksi embedding
	// hanya dipakai bila semuanya punya, supaya skor antar skill sebanding.
	allVectors bool
}

// Decision menjelaskan bagaimana skill aktif sebuah percakapan memengaruhi
// pilihan skill pada pesan berikutnya.
type Decision string

const (
	// DecisionNone: percakapan belum punya skill aktif; pilihan murni dari seleksi.
	DecisionNone Decision = ""
	// DecisionKeep: skill aktif dipertahankan.
	DecisionKeep Decision = "keep"
	// DecisionSwitch: skill aktif diganti karena skill lain jauh lebih cocok.
	DecisionSwitch Decision = "switch"
)

// Metode seleksi.
const (
	MethodBM25      = "bm25"      // kecocokan kata
	MethodEmbedding = "embedding" // kemiripan makna
	MethodFallback  = "fallback"  // diputuskan pemilih cadangan (model) karena skor ambigu
)

// Match adalah satu skill hasil pencarian beserta skornya. Skala skor
// bergantung metode: BM25 (0 ke atas) atau kemiripan kosinus (-1..1).
type Match struct {
	Entry IndexEntry
	Score float64
}

// Selection adalah hasil seleksi skill untuk satu pesan.
type Selection struct {
	// Best adalah skill terpilih; nil bila tidak ada yang cocok.
	Best *Match

	// Close adalah skill yang skornya dekat dengan skor teratas (termasuk
	// skill teratas). Skill aktif percakapan yang ada di sini dipertahankan,
	// karena skill lain tidak "jauh lebih cocok".
	Close []Match

	// Method adalah metode seleksi (MethodBM25/MethodEmbedding/MethodFallback);
	// "" bila tidak ada pesan untuk diseleksi.
	Method string

	// FallbackTokens adalah token yang dipakai pemilih cadangan (0 bila tidak dipanggil).
	FallbackTokens int

	// Degraded true bila embedding atau pemilih cadangan gagal sehingga hasil
	// memakai jalur cadangan. Hasil seperti ini tidak disimpan di cache.
	Degraded bool
}

// vectorCache adalah isi file .embedding.json di folder skill: embedding
// disimpan bersama hash teks dan nama model, supaya hanya dihitung ulang bila
// isi skill atau modelnya berubah.
type vectorCache struct {
	Model  string    `json:"model"`
	Hash   string    `json:"hash"`
	Vector []float32 `json:"vector"`
}
