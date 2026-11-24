package proxy

import (
	"errors"

	"github.com/Bauagg/middleware-skill-ai/internal/services/memorynote"
)

// ErrModelRequired dikembalikan Prepare bila body chat tidak membawa "model".
// Setiap user memakai model 9Router miliknya sendiri, jadi middleware tidak
// menebak model bawaan.
var ErrModelRequired = errors.New("field model wajib diisi")

// RequestMeta adalah data dari header request yang dibutuhkan untuk mengenali percakapan.
type RequestMeta struct {
	// UserID dari header X-User-Id (wajib untuk chat).
	UserID string

	// ConversationID adalah header X-Conversation-Id dari client (opsional).
	// Tanpa ini, percakapan dikenali dari pesan user pertamanya.
	ConversationID string
}

// Prepared adalah hasil persiapan body request sebelum diteruskan ke 9Router.
type Prepared struct {
	// Body adalah body yang akan dikirim. Sama persis dengan aslinya bila tidak
	// ada skill yang disisipkan.
	Body []byte

	// UserID pengirim request (header X-User-Id).
	UserID string

	// Model dan Stream dibaca dari body request ("" / false bila tidak ada).
	Model  string
	Stream bool

	// BudgetTokens adalah batas token skill untuk model ini (0 = tanpa batas).
	BudgetTokens int

	// SelectionCache dan SessionCache adalah hasil cache ("hit", "miss", "off",
	// dan khusus sesi juga "switch"); "" bila tidak berlaku.
	SelectionCache string
	SessionCache   string

	// SelectionMethod: "bm25", "embedding", atau "fallback" (pemilih cadangan);
	// "" bila tidak ada pesan untuk diseleksi. FallbackTokens adalah token yang
	// dipakai pemilih cadangan pada request ini (0 bila tidak dipanggil).
	SelectionMethod string
	FallbackTokens  int

	// Terisi hanya bila ada skill yang disisipkan. ExtraTokensEst adalah
	// perkiraan token tambahan (karakter / 4); SkillVariant adalah
	// "full", "short", atau "truncated".
	Skill          string
	Score          float64
	SkillVariant   string
	ExtraTokensEst int

	// ContextPackTokensEst dan MemoryTokensEst adalah perkiraan token context
	// pack dan catatan memori yang disisipkan (0 bila tidak ada).
	// MemoryNoteIDs adalah id catatan yang disisipkan.
	ContextPackTokensEst int
	MemoryTokensEst      int
	MemoryNoteIDs        []string

	// ToolNames adalah tool MCP yang dikirim ke model (kosong = request
	// diteruskan biasa tanpa putaran tool). ToolsTokensEst adalah perkiraan
	// token definisinya. ToolCalls dan ToolSteps diisi selama putaran tool:
	// nama tool yang dipanggil dan berapa kali model dipanggil.
	ToolNames      []string
	ToolsTokensEst int
	ToolCalls      []string
	ToolSteps      int

	// Query adalah pesan user terakhir dan Recent beberapa pesan sebelumnya;
	// diserahkan ke memori setelah respons selesai (lihat Service.Learn).
	Query  string
	Recent []memorynote.Turn
}

// resetInjection mengosongkan catatan penyisipan bila body gagal disusun
// (request diteruskan apa adanya, jadi log tidak boleh mengaku ada sisipan).
func (p *Prepared) resetInjection() {
	p.ExtraTokensEst, p.ContextPackTokensEst, p.MemoryTokensEst = 0, 0, 0
	p.MemoryNoteIDs = nil
}
