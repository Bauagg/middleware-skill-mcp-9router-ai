// Package contextpack mengelola context pack: ringkasan tetap yang selalu
// relevan (gambaran bisnis, istilah, aturan tetap) dan disisipkan ke prompt
// setelah skill. Disimpan di PostgreSQL (m_context_packs).
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
)

const (
	// cacheTTL: teks context pack aktif disimpan sebentar di memori, supaya
	// tidak ada query database di setiap request; perubahan lewat API langsung
	// mengosongkan cache.
	cacheTTL = 30 * time.Second

	lookupTimeout = 2 * time.Second

	truncMarker = "\n[... context pack dipotong karena batas token]"
)

// Service mengelola context pack.
type Service struct {
	repo      *Repository
	maxTokens int // batas token gabungan context pack aktif (0 = tanpa batas)

	mu       sync.Mutex
	cached   string
	cachedAt time.Time
}

// NewService membuat service. maxTokens adalah batas token gabungan semua
// context pack aktif yang disisipkan ke prompt.
func NewService(repo *Repository, maxTokens int) *Service {
	return &Service{repo: repo, maxTokens: maxTokens}
}

// List mengambil semua context pack.
func (s *Service) List() ([]ContextPack, error) {
	rows, err := s.repo.List()
	if err != nil {
		logger.Error("baca context pack gagal", zap.Error(err))
		return nil, apperror.NewInternalServerError("Gagal mengambil context pack")
	}
	if rows == nil {
		rows = []ContextPack{}
	}
	return rows, nil
}

// Get mengambil satu context pack.
func (s *Service) Get(id string) (*ContextPack, error) {
	row, err := s.repo.Get(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NewNotFound("Context pack tidak ditemukan")
	}
	return row, err
}

// Create menyimpan context pack baru.
func (s *Service) Create(in CreateInput) (*ContextPack, error) {
	row := &ContextPack{Name: strings.TrimSpace(in.Name), Content: strings.TrimSpace(in.Content), Active: true}
	if in.Active != nil {
		row.Active = *in.Active
	}
	if err := s.repo.Create(row); err != nil {
		return nil, err // nama ganda dipetakan apperror.Handle menjadi 409
	}
	s.invalidate()
	return row, nil
}

// Update mengubah context pack.
func (s *Service) Update(id string, in UpdateInput) (*ContextPack, error) {
	if _, err := s.Get(id); err != nil {
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
		if err := s.repo.Update(id, fields); err != nil {
			return nil, err
		}
		s.invalidate()
	}
	return s.Get(id)
}

// Delete menghapus context pack.
func (s *Service) Delete(id string) error {
	n, err := s.repo.Delete(id)
	if err != nil {
		return err
	}
	if n == 0 {
		return apperror.NewNotFound("Context pack tidak ditemukan")
	}
	s.invalidate()
	return nil
}

// ActiveText mengembalikan gabungan context pack aktif yang siap disisipkan
// ("" bila tidak ada), sudah dipotong sesuai batas token. Bila database gagal
// dibaca, teks terakhir yang tersimpan di cache tetap dipakai.
func (s *Service) ActiveText(ctx context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < cacheTTL {
		return s.cached
	}

	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	rows, err := s.repo.Active(ctx)
	if err != nil {
		logger.Warn("baca context pack aktif gagal, memakai cache lama", zap.Error(err))
		return s.cached
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

	s.cached, s.cachedAt = text, time.Now()
	return text
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.cachedAt = time.Time{}
	s.mu.Unlock()
}

// Block membungkus teks context pack dengan format yang selalu sama sebelum
// disisipkan ke system prompt.
func Block(text string) string {
	return "<context_pack>\n" + text + "\n</context_pack>"
}
