// Package database mengurus koneksi GORM ke PostgreSQL dan migrasi skema.
// Ini padanan Go dari `src/databases/index.ts` (Sequelize) di template Express.
package database

import (
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/Bauagg/middleware-skill-ai/internal/config"
)

// Connect membuka koneksi ke PostgreSQL dan mengatur connection pool.
func Connect(cfg config.DBConfig) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("gagal konek ke database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("gagal mengambil sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	return db, nil
}

// Migrate menjalankan AutoMigrate untuk semua model yang terdaftar.
// Analogi dengan `sequelize.sync({ alter: true })` di template Express.
// Catatan: untuk produksi yang lebih terkontrol, pertimbangkan migration
// eksplisit (mis. golang-migrate) alih-alih AutoMigrate.
func Migrate(db *gorm.DB, models ...interface{}) error {
	if len(models) == 0 {
		return nil
	}
	return db.AutoMigrate(models...)
}
