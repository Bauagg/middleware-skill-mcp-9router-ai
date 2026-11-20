// Package router mengagregasi route aplikasi pada engine Gin.
// Ini padanan Go dari `src/router/indext.ts` di template Express.
//
// Endpoint inti middleware: proxy `/v1/*` ke 9Router (skill, context pack, dan
// catatan memori disisipkan pada `/v1/chat/completions`), API skill di
// `/api/skills`, log pemakaian di `/api/usage-logs`, eval skill di
// `/api/evals`, context pack di `/api/context-packs`, catatan memori di
// `/api/memory-notes`, dan koneksi MCP (Figma, Canva, ...) di `/api/mcp`.
package router

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/config"
	"github.com/Bauagg/middleware-skill-ai/internal/database"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/secretbox"
	"github.com/Bauagg/middleware-skill-ai/internal/services/contextpack"
	"github.com/Bauagg/middleware-skill-ai/internal/services/eval"
	"github.com/Bauagg/middleware-skill-ai/internal/services/mcp"
	"github.com/Bauagg/middleware-skill-ai/internal/services/memorynote"
	"github.com/Bauagg/middleware-skill-ai/internal/services/proxy"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillcache"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillfallback"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillindex"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillstore"
	"github.com/Bauagg/middleware-skill-ai/internal/services/usagelog"
)

// Register menyiapkan semua route aplikasi dan menyambungkan antar service
// (wiring terpusat). db dipakai untuk log pemakaian dan hasil eval di PostgreSQL.
//
// Fungsi yang dikembalikan dipanggil saat server dimatikan, setelah request
// terakhir selesai: menulis sisa log pemakaian ke database.
func Register(r *gin.Engine, db *gorm.DB, cfg *config.Config) (shutdown func(ctx context.Context)) {
	// tabel transaksi dibuat/diperbarui otomatis saat start
	if err := database.Migrate(db,
		&usagelog.UsageLog{}, &eval.EvalRun{},
		&contextpack.ContextPack{}, &memorynote.MemoryNote{},
		&mcp.Credential{},
	); err != nil {
		logger.Fatal("migrasi database gagal", zap.Error(err))
	}

	// kolom + index pencarian teks catatan memori (generated column, di luar AutoMigrate)
	noteRepo := memorynote.NewRepository(db)
	if err := noteRepo.Migrate(); err != nil {
		logger.Fatal("migrasi pencarian catatan memori gagal", zap.Error(err))
	}

	api := r.Group("/api")
	api.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "pong"})
	})

	// layanan luar: 9Router (proxy + panggilan middleware sendiri) dan Redis
	nineRouter := clients.NewNineRouter(cfg.Router.URL, cfg.Router.APIKey)
	redis := newRedis(cfg)
	cache := skillcache.NewService(redis, cfg.Skill.SelectionTTL, cfg.Skill.SessionTTL)

	// skills: folder skill = sumber utama, tanpa database (lihat dokumen desain).
	// skillindex membaca folder yang sama untuk membangun index pencarian.
	skillRepo := skillstore.NewRepository(cfg.App.UploadPath)
	indexService := skillindex.NewService(skillRepo, skillindex.NewRepository(),
		skillindex.Options{
			MinScore:        cfg.Skill.MinScore,
			SwitchRatio:     cfg.Skill.SwitchRatio,
			AmbiguousRatio:  cfg.Skill.AmbiguousRatio,
			MinSimilarity:   cfg.Skill.MinSimilarity,
			SwitchMargin:    cfg.Skill.SwitchMargin,
			AmbiguousMargin: cfg.Skill.AmbiguousMargin,
			EmbedTimeout:    cfg.Embedding.Timeout,
		},
		skillindex.Deps{
			Embedder: newEmbedder(cfg),
			Fallback: newFallback(cfg, nineRouter),
		},
	)

	// muat index sekali saat start; errornya sudah dicatat oleh Reload, dan index
	// kosong tidak menghentikan server (request tetap diteruskan tanpa skill)
	_ = indexService.Reload()

	// file yang diubah langsung di disk dimuat ulang otomatis (0 = dimatikan)
	go indexService.Watch(context.Background(), cfg.Skill.WatchInterval)

	// index juga dibangun ulang segera setiap kali upload skill berhasil
	skillService := skillstore.NewService(skillRepo, func() { _ = indexService.Reload() })

	skillsGroup := api.Group("/skills")
	skillstore.RegisterRoutes(skillsGroup, skillstore.NewController(skillService, cfg.Skill.Budgets))
	skillindex.RegisterRoutes(skillsGroup, skillindex.NewController(indexService))

	// log pemakaian: dicatat proxy di latar belakang, dibaca lewat /api/usage-logs
	usageService := usagelog.NewService(usagelog.NewRepository(db))
	usagelog.RegisterRoutes(api.Group("/usage-logs"), usagelog.NewController(usageService))

	// eval skill: soal di skills/<nama>/EVAL.yaml, hasil di t_eval_runs
	evalService := eval.NewService(eval.Deps{
		LLM:         nineRouter,
		Renderer:    skillService,
		Files:       skillRepo,
		Store:       eval.NewRepository(db),
		TargetModel: cfg.Eval.TargetModel,
		JudgeModel:  cfg.Eval.JudgeModel,
		Concurrency: cfg.Eval.Concurrency,
	})
	eval.RegisterRoutes(api.Group("/evals"), eval.NewController(evalService))

	// memori (Tahap 4): context pack (m_context_packs) selalu disisipkan,
	// catatan memori (t_memory_notes) hanya yang cocok dengan pesan user
	packService := contextpack.NewService(contextpack.NewRepository(db), cfg.Memory.ContextPackMaxTokens)
	contextpack.RegisterRoutes(api.Group("/context-packs"), contextpack.NewController(packService))

	noteService := memorynote.NewService(noteRepo, nineRouter, memorynote.Options{
		MaxNotes:           cfg.Memory.MaxNotes,
		NoteMaxTokens:      cfg.Memory.NoteMaxTokens,
		MinMatch:           cfg.Memory.MinMatch,
		LookupTimeout:      cfg.Memory.LookupTimeout,
		NoteTTL:            cfg.Memory.NoteTTL,
		ExtractModel:       cfg.Memory.ExtractModel,
		ExtractTimeout:     cfg.Memory.ExtractTimeout,
		ExtractConcurrency: cfg.Memory.ExtractConcurrency,
	})
	if noteService.ExtractEnabled() {
		logger.Info("ekstraksi memori aktif", zap.String("model", cfg.Memory.ExtractModel))
	} else {
		logger.Info("MEMORY_EXTRACT_MODEL kosong: ekstraksi memori dimatikan (catatan manual tetap bisa)")
	}
	memorynote.RegisterRoutes(api.Group("/memory-notes"), memorynote.NewController(noteService))

	// MCP: middleware menjadi klien MCP (Figma, Canva, ...) karena 9Router tidak bisa
	mcpService := newMCP(cfg, db, redis)
	mcp.RegisterRoutes(api.Group("/mcp"), mcp.NewController(mcpService))
	var tools proxy.Tools
	if mcpService.Enabled() {
		tools = mcpService
		go mcpService.Run(context.Background())
	}

	// proxy ke 9Router: client OpenAI-compatible cukup mengarah ke middleware ini
	proxyService := proxy.NewService(proxy.Deps{
		Selector:     indexService,
		Renderer:     skillService,
		Cache:        cache,
		Upstream:     nineRouter,
		Recorder:     usageService,
		ContextPacks: packService,
		Memory:       noteService,
		Tools:        tools,
		ToolMaxSteps: cfg.MCP.MaxSteps,
		Budgets:      cfg.Skill.Budgets,
	})
	proxy.RegisterRoutes(r, proxy.NewController(proxyService))

	return func(ctx context.Context) {
		usageService.Close(ctx)
	}
}

// newRedis menyambung ke Redis (cache skill dan state login MCP). Redis wajib
// terhubung saat start (seperti database): bila tidak terjangkau, server
// berhenti. Setelah berjalan, Redis yang mati tidak menghentikan request: cache
// dilewati (status "off") sampai Redis pulih.
func newRedis(cfg *config.Config) *clients.Redis {
	rc := clients.NewRedis(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Redis.Timeout)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rc.Ping(ctx); err != nil {
		logger.Fatal("redis connection failed", zap.String("addr", cfg.Redis.Addr), zap.Error(err))
	}
	logger.Info("redis connected", zap.String("addr", cfg.Redis.Addr))
	return rc
}

// newMCP membaca daftar server MCP dari MCP_CONFIG_PATH. File tidak ada =
// MCP dimatikan. Bila ada server aktif, MCP_TOKEN_KEY wajib diisi (token login
// disimpan terenkripsi); server berhenti bila tidak.
func newMCP(cfg *config.Config, db *gorm.DB, kv mcp.KV) *mcp.Service {
	repo := mcp.NewRepository(db, kv)
	fileCfg, found, err := repo.LoadConfig(cfg.MCP.ConfigPath)
	if err != nil {
		logger.Fatal("konfigurasi MCP tidak valid", zap.Error(err))
	}
	if !found {
		logger.Info("file MCP tidak ada: MCP dimatikan", zap.String("path", cfg.MCP.ConfigPath))
	}

	var box *secretbox.Box
	enabled := 0
	for _, s := range fileCfg.Servers {
		if s.IsEnabled() {
			enabled++
		}
	}
	if enabled > 0 {
		if box, err = secretbox.New(cfg.MCP.TokenKey); err != nil {
			logger.Fatal("MCP_TOKEN_KEY tidak valid (wajib bila ada server MCP aktif)", zap.Error(err))
		}
		logger.Info("mcp aktif", zap.Int("servers", enabled))
	}

	return mcp.NewService(repo, box, fileCfg, mcp.Options{
		MaxTools:        cfg.MCP.MaxTools,
		MinScore:        cfg.MCP.MinScore,
		ToolTimeout:     cfg.MCP.ToolTimeout,
		ResultMaxTokens: cfg.MCP.ResultMaxTokens,
		RefreshInterval: cfg.MCP.RefreshInterval,
		BaseURL:         cfg.App.BaseURL,
	})
}

// newEmbedder membuat klien embedding bila EMBEDDING_MODEL diisi; nil berarti
// embedding dimatikan (seleksi memakai BM25). Bila diisi, layanan embedding
// wajib terjangkau saat start: server berhenti bila tidak. Setelah berjalan,
// embedding yang gagal membuat seleksi kembali ke BM25 untuk request itu.
func newEmbedder(cfg *config.Config) skillindex.Embedder {
	if cfg.Embedding.Model == "" {
		logger.Info("EMBEDDING_MODEL kosong: seleksi skill memakai BM25")
		return nil
	}

	emb := clients.NewEmbedder(cfg.Embedding.BaseURL, cfg.Embedding.APIKey, cfg.Embedding.Model)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := emb.Embed(ctx, []string{"cek koneksi embedding"}); err != nil {
		logger.Fatal("embedding connection failed",
			zap.String("url", cfg.Embedding.BaseURL.String()), zap.String("model", cfg.Embedding.Model), zap.Error(err))
	}
	logger.Info("embedding connected", zap.String("model", cfg.Embedding.Model))
	return emb
}

// newFallback membuat pemilih cadangan bila SKILL_FALLBACK_MODEL diisi; nil
// berarti dimatikan (skor ambigu diputuskan dari skor saja).
func newFallback(cfg *config.Config, llm skillfallback.Completer) skillindex.Fallback {
	if cfg.Skill.FallbackModel == "" {
		logger.Info("SKILL_FALLBACK_MODEL kosong: pemilih cadangan dimatikan")
		return nil
	}
	logger.Info("pemilih cadangan aktif", zap.String("model", cfg.Skill.FallbackModel))
	return skillfallback.NewService(llm, cfg.Skill.FallbackModel, cfg.Skill.FallbackTimeout)
}
