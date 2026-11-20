// Package skillcache menyimpan dua cache di Redis untuk pemilihan skill:
//
//   - cache sesi: skill aktif tiap percakapan, supaya pesan lanjutan ("oke
//     lanjut", "yang tadi") tetap memakai skill yang sama dan seleksi tidak
//     diulang di setiap pesan;
//   - cache hasil seleksi: pesan (setelah dinormalisasi) -> skill terpilih,
//     berlaku untuk satu versi index.
//
// Cache hanya optimasi. Bila Redis mati atau dimatikan, semua pencarian
// dianggap tidak ada (state Off) dan request tetap berjalan normal.
package skillcache

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/bm25"
)

// Service mengurus normalisasi key dan masa berlaku cache.
type Service struct {
	repo       *Repository
	selTTL     time.Duration
	sessionTTL time.Duration
}

// NewService membuat service. kv nil = cache dimatikan.
func NewService(kv KV, selectionTTL, sessionTTL time.Duration) *Service {
	return &Service{repo: NewRepository(kv), selTTL: selectionTTL, sessionTTL: sessionTTL}
}

// GetSelection mencari hasil seleksi untuk query pada versi index tertentu.
func (s *Service) GetSelection(ctx context.Context, version, query string) (Selection, State) {
	key := queryKey(query)
	if key == "" || !s.repo.Enabled() {
		return Selection{}, Off
	}
	return s.repo.GetSelection(ctx, version, key)
}

// SetSelection menyimpan hasil seleksi untuk query pada versi index tertentu.
func (s *Service) SetSelection(ctx context.Context, version, query string, sel Selection) {
	key := queryKey(query)
	if key == "" || !s.repo.Enabled() {
		return
	}
	s.repo.SetSelection(ctx, version, key, sel, s.selTTL)
}

// GetSession mencari skill aktif sebuah percakapan.
func (s *Service) GetSession(ctx context.Context, conversationKey string) (string, State) {
	if conversationKey == "" || !s.repo.Enabled() {
		return "", Off
	}
	return s.repo.GetSession(ctx, conversationKey)
}

// SetSession menyimpan (atau memperpanjang) skill aktif sebuah percakapan.
func (s *Service) SetSession(ctx context.Context, conversationKey, dir string) {
	if conversationKey == "" || dir == "" || !s.repo.Enabled() {
		return
	}
	s.repo.SetSession(ctx, conversationKey, dir, s.sessionTTL)
}

// queryKey menormalkan pesan: kata isi (tanpa stopword dan sapaan), unik,
// terurut, lalu di-hash. Pesan yang berbeda hanya urutan kata atau kata
// umumnya menghasilkan key yang sama. "" bila tidak ada kata isi.
func queryKey(query string) string {
	tokens := bm25.Tokenize(query)
	if len(tokens) == 0 {
		return ""
	}

	slices.Sort(tokens)
	tokens = slices.Compact(tokens)

	sum := sha1.Sum([]byte(strings.Join(tokens, " ")))
	return hex.EncodeToString(sum[:])
}

// ConversationKey membentuk pengenal percakapan. Format OpenAI tidak punya ID
// percakapan, jadi dipakai ID dari client bila ada (header X-Conversation-Id),
// atau pesan user pertama (yang selalu ikut terkirim di setiap request pada
// percakapan yang sama). Header Authorization ikut di-hash supaya percakapan
// dua pengguna berbeda tidak bertabrakan; hanya hash-nya yang tersimpan, bukan
// isinya. "" bila tidak ada bahan sama sekali.
func ConversationKey(authorization, conversationID, firstUserText string) string {
	seed := strings.TrimSpace(conversationID)
	if seed == "" {
		seed = strings.TrimSpace(firstUserText)
	}
	if seed == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(authorization + "\x00" + seed))
	return hex.EncodeToString(sum[:16])
}
