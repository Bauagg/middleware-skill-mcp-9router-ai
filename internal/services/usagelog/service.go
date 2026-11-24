// Package usagelog mencatat pemakaian tiap request ke PostgreSQL (tabel
// t_usage_logs) dan menyediakan daftar serta ringkasannya. Tujuannya mengukur
// berapa token tambahan yang dipakai skill dan seberapa sering skill terpilih.
package usagelog

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
)

const (
	queueSize     = 1024
	batchSize     = 100
	flushInterval = 2 * time.Second
)

// Service mencatat dan membaca log pemakaian.
type Service struct {
	repo  *Repository
	queue chan UsageLog
	stop  chan struct{} // ditutup Close: worker menulis sisa antrean lalu berhenti
	done  chan struct{} // ditutup worker setelah sisa antrean tertulis
}

// NewService membuat service dan menjalankan worker penulis di latar belakang.
func NewService(repo *Repository) *Service {
	s := &Service{
		repo:  repo,
		queue: make(chan UsageLog, queueSize),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go s.run()
	return s
}

// Close menulis sisa antrean ke database lalu menghentikan worker. Dipanggil
// saat server dimatikan (mis. docker stop saat deploy), setelah request
// terakhir selesai, supaya catatannya tidak hilang. Menunggu paling lama
// sampai ctx habis.
func (s *Service) Close(ctx context.Context) {
	close(s.stop)
	select {
	case <-s.done:
	case <-ctx.Done():
		logger.Warn("usage log belum selesai ditulis saat server berhenti")
	}
}

// Record menaruh catatan ke antrean untuk ditulis ke database. Tidak pernah
// menunda atau menggagalkan request: bila antrean penuh, catatan dibuang dan
// dicatat di log aplikasi.
func (s *Service) Record(e Entry) {
	select {
	case s.queue <- toModel(e):
	default:
		logger.Warn("antrean usage log penuh, catatan dibuang")
	}
}

// run menulis catatan ke database secara berkelompok (per 100 baris atau tiap
// 2 detik), supaya database tidak ditulis satu per satu di setiap request.
// Saat Close dipanggil, sisa antrean ditulis dulu; catatan hanya bisa hilang
// bila proses dihentikan paksa (kill -9).
func (s *Service) run() {
	batch := make([]UsageLog, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.repo.CreateBatch(batch); err != nil {
			logger.Error("simpan usage log gagal", zap.Int("rows", len(batch)), zap.Error(err))
		}
		batch = batch[:0]
	}

	for {
		select {
		case l := <-s.queue:
			batch = append(batch, l)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-s.stop:
			for {
				select {
				case l := <-s.queue:
					batch = append(batch, l)
					if len(batch) >= batchSize {
						flush()
					}
				default:
					flush()
					close(s.done)
					return
				}
			}
		}
	}
}

// List mengambil log pemakaian terbaru lebih dulu.
func (s *Service) List(f ListFilter) ([]UsageLog, int64, error) {
	rows, total, err := s.repo.List(f)
	if err != nil {
		logger.Error("baca usage log gagal", zap.Error(err))
		return nil, 0, apperror.NewInternalServerError("Gagal mengambil log pemakaian")
	}
	return rows, total, nil
}

// Summary meringkas pemakaian per skill, status cache, dan varian skill.
func (s *Service) Summary(f SummaryFilter) (*SummaryResult, error) {
	res, err := s.repo.Summary(f)
	if err != nil {
		logger.Error("ringkas usage log gagal", zap.Error(err))
		return nil, apperror.NewInternalServerError("Gagal meringkas log pemakaian")
	}
	return &res, nil
}

// toModel mengubah Entry menjadi baris tabel.
func toModel(e Entry) UsageLog {
	model := e.Model
	if model == "" {
		model = "unknown"
	}
	return UsageLog{
		UserID:          e.UserID,
		Model:           model,
		Path:            e.Path,
		Stream:          e.Stream,
		SkillName:       e.SkillName,
		SkillScore:      e.SkillScore,
		ExtraTokensEst:  e.ExtraTokensEst,
		SkillVariant:    e.SkillVariant,
		BudgetTokens:    e.BudgetTokens,
		SelectionCache:  e.SelectionCache,
		SessionCache:    e.SessionCache,
		SelectionMethod: e.SelectionMethod,
		FallbackTokens:  e.FallbackTokens,

		ContextPackTokensEst: e.ContextPackTokensEst,
		MemoryNotes:          e.MemoryNotes,
		MemoryNoteIDs:        e.MemoryNoteIDs,
		MemoryTokensEst:      e.MemoryTokensEst,

		MCPToolsOffered:   e.MCPToolsOffered,
		MCPToolsTokensEst: e.MCPToolsTokensEst,
		MCPToolCalls:      e.MCPToolCalls,
		MCPToolNames:      e.MCPToolNames,
		MCPSteps:          e.MCPSteps,

		Status:           e.Status,
		PromptTokens:     e.PromptTokens,
		CompletionTokens: e.CompletionTokens,
		CachedTokens:     e.CachedTokens,
		LatencyMs:        e.LatencyMs,
		Aborted:          e.Aborted,
	}
}
