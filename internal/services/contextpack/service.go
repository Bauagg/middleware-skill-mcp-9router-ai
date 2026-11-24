// Package contextpack mengelola context pack: ringkasan tetap yang selalu
// relevan (gambaran bisnis, istilah, aturan tetap) milik tiap user, disisipkan
// ke prompt setelah skill. Disimpan di PostgreSQL (m_context_packs).
package contextpack

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/tokens"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

const (
	// cacheTTL: teks context pack aktif disimpan sebentar di memori, supaya
	// tidak ada query database di setiap request; perubahan lewat API langsung
	// mengosongkan cache.
	cacheTTL = 30 * time.Second

	lookupTimeout = 2 * time.Second

	truncMarker = "\n[... context pack dipotong karena batas token]"
)

// Service mengelola context pack per user.
type Service struct {
	repo      *Repository
	maxTokens int // batas token gabungan context pack aktif (0 = tanpa batas)

	mu    sync.Mutex
	cache map[string]cachedText // per user
}

type cachedText struct {
	text string
	at   time.Time
}

// NewService membuat service. maxTokens adalah batas token gabungan semua
// context pack aktif yang disisipkan ke prompt.
func NewService(repo *Repository, maxTokens int) *Service {
	return &Service{repo: repo, maxTokens: maxTokens, cache: map[string]cachedText{}}
}

// List mengambil semua context pack milik user.
func (s *Service) List(userID string) ([]ContextPack, error) {
	rows, err := s.repo.List(userID)
	if err != nil {
		logger.Error("baca context pack gagal", zap.Error(err))
		return nil, apperror.NewInternalServerError("Gagal mengambil context pack")
	}
	if rows == nil {
		rows = []ContextPack{}
	}
	return rows, nil
}

// Get mengambil satu context pack milik user.
func (s *Service) Get(userID, id string) (*ContextPack, error) {
	row, err := s.repo.Get(userID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NewNotFound("Context pack tidak ditemukan")
	}
	return row, err
}

// Create menyimpan context pack baru milik user.
func (s *Service) Create(userID string, in CreateInput) (*ContextPack, error) {
	row := &ContextPack{UserID: userID, Name: strings.TrimSpace(in.Name), Content: strings.TrimSpace(in.Content), Active: true}
	if in.Active != nil {
		row.Active = *in.Active
	}
	if err := s.repo.Create(row); err != nil {
		return nil, err // nama ganda dipetakan apperror.Handle menjadi 409
	}
	s.invalidate(userID)
	return row, nil
}

// Update mengubah context pack milik user.
func (s *Service) Update(userID, id string, in UpdateInput) (*ContextPack, error) {
	cur, err := s.Get(userID, id)
	if err != nil {
		return nil, err
	}

	fields := map[string]any{}
	if in.Name != nil {
		fields["name"] = strings.TrimSpace(*in.Name)
	}
	if in.Content != nil {
		fields["content"] = strings.TrimSpace(*in.Content)
	}
	if in.Active != nil {
		fields["active"] = *in.Active
	}
	if len(fields) > 0 {
		if err := s.repo.Update(userID, id, fields); err != nil {
			return nil, err
		}
		// admin bisa mengubah milik user lain: cache pemiliknya yang dibuang
		s.invalidate(cur.UserID)
	}
	return s.Get(userID, id)
}

// Delete menghapus context pack milik user.
func (s *Service) Delete(userID, id string) error {
	cur, err := s.Get(userID, id)
	if err != nil {
		return err
	}
	n, err := s.repo.Delete(userID, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return apperror.NewNotFound("Context pack tidak ditemukan")
	}
	s.invalidate(cur.UserID)
	return nil
}

// ActiveText mengembalikan gabungan context pack aktif milik user di ctx
// (header X-User-Id) yang siap disisipkan ("" bila tidak ada), sudah dipotong
// sesuai batas token. Bila database gagal dibaca, teks terakhir di cache tetap
// dipakai.
func (s *Service) ActiveText(ctx context.Context) string {
	userID := userctx.From(ctx)
	if userID == "" {
		return ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if c, ok := s.cache[userID]; ok && time.Since(c.at) < cacheTTL {
		return c.text
	}

	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	rows, err := s.repo.Active(ctx, userID)
	if err != nil {
		logger.Warn("baca context pack aktif gagal, memakai cache lama", zap.Error(err))
		return s.cache[userID].text
	}

	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, "## "+r.Name+"\n"+r.Content)
	}
	text := strings.Join(parts, "\n\n")
	if s.maxTokens > 0 {
		if cut, ok := tokens.Truncate(text, s.maxTokens); ok {
			text = cut + truncMarker
		}
	}

	s.cache[userID] = cachedText{text: text, at: time.Now()}
	return text
}

func (s *Service) invalidate(userID string) {
	s.mu.Lock()
	delete(s.cache, userID)
	s.mu.Unlock()
}

// Block membungkus teks context pack dengan format yang selalu sama sebelum
// disisipkan ke system prompt.
func Block(text string) string {
	return "<context_pack>\n" + text + "\n</context_pack>"
}
