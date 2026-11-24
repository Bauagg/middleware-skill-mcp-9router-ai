// Package main adalah entry point server.
// Ini padanan Go dari `src/index.ts` di template Express:
// muat config -> init logger -> konek DB -> pasang middleware global
// -> daftarkan route -> jalankan server.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/config"
	"github.com/Bauagg/middleware-skill-ai/internal/database"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/middleware"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/jwtauth"
	"github.com/Bauagg/middleware-skill-ai/internal/router"
)

// shutdownTimeout adalah batas waktu menunggu request yang sedang berjalan
// (termasuk chat streaming) saat server dimatikan. Harus lebih pendek dari
// stop_grace_period di docker-compose.yml (30 detik), supaya Docker tidak
// membunuh proses sebelum selesai.
const shutdownTimeout = 25 * time.Second

// ensureDir membuat folder (beserta induknya) bila belum ada dan mencatat ke log.
func ensureDir(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	logger.Info("folder created", zap.String("path", path))
	return nil
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[config] %v", err)
	}

	if err := logger.Init(cfg.Log.Level, cfg.Log.Dir, cfg.App.Env); err != nil {
		log.Fatalf("[logger] %v", err)
	}
	defer logger.Sync()

	// folder skill tidak ikut di-commit (gitignore), jadi dibuat sendiri bila belum ada
	if err := ensureDir(cfg.App.UploadPath); err != nil {
		logger.Fatal("failed to prepare skills folder", zap.String("path", cfg.App.UploadPath), zap.Error(err))
	}

	gin.SetMode(cfg.App.GinMode)

	// Koneksi GORM disiapkan sebagai fondasi data (dipakai pada tahap
	// middleware berikutnya: log pemakaian, memori, dsb).
	db, err := database.Connect(cfg.DB)
	if err != nil {
		logger.Fatal("database connection failed", zap.Error(err))
	}
	logger.Info("database connected")

	// token login (register/login di middleware) untuk user tanpa aplikasi pihak ketiga
	tokens, err := jwtauth.NewIssuer(cfg.Auth.JWTSecret, cfg.Auth.AccessTTL, cfg.Auth.RefreshTTL)
	if err != nil {
		logger.Fatal("token login tidak bisa disiapkan", zap.Error(err))
	}

	r := gin.New()
	// Auth setelah CORS: preflight OPTIONS dijawab CORS tanpa butuh key/token
	r.Use(middleware.Recovery(), middleware.HTTPLogger(), middleware.CORS(), middleware.Auth(cfg.App.APIKeys, tokens))

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "Server is running"})
	})
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	cleanup := router.Register(r, db, cfg, tokens)

	// http.Server (bukan r.Run) supaya bisa dimatikan dengan rapi: saat
	// SIGTERM (docker stop / deploy ulang), request baru ditolak, request yang
	// sedang berjalan ditunggu selesai, lalu sisa log pemakaian ditulis.
	// Tanpa WriteTimeout karena respons streaming bisa berlangsung lama.
	srv := &http.Server{
		Addr:              ":" + cfg.App.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("server starting", zap.String("addr", srv.Addr), zap.String("env", cfg.App.Env))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	logger.Info("server stopping: menunggu request yang sedang berjalan")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("sebagian request belum selesai saat server berhenti", zap.Error(err))
	}
	cleanup(shutdownCtx)
	logger.Info("server stopped")
}
