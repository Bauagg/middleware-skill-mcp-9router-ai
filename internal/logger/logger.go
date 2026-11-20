// Package logger menyediakan logger terstruktur berbasis zap.
// Ini padanan Go dari Winston di template Express: combined.log (info+),
// error.log (error+), dan output console berwarna saat bukan production.
package logger

import (
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// log adalah logger global (diakses lewat L() dan fungsi helper di bawah).
var log *zap.Logger = zap.NewNop()

// Init membangun logger. level: debug|info|warn|error. dir: folder log.
// env: "production" mematikan output console.
func Init(level, dir, env string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	lvl := zapcore.InfoLevel
	_ = lvl.UnmarshalText([]byte(strings.ToLower(strings.TrimSpace(level))))

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "time"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encCfg.EncodeLevel = zapcore.LowercaseLevelEncoder

	fileEncoder := zapcore.NewJSONEncoder(encCfg)
	consoleEncoder := zapcore.NewConsoleEncoder(encCfg)

	combinedFile, err := os.OpenFile(filepath.Join(dir, "combined.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	errorFile, err := os.OpenFile(filepath.Join(dir, "error.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	cores := []zapcore.Core{
		zapcore.NewCore(fileEncoder, zapcore.AddSync(combinedFile), lvl),
		zapcore.NewCore(fileEncoder, zapcore.AddSync(errorFile), zapcore.ErrorLevel),
	}
	if env != "production" {
		cores = append(cores, zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), lvl))
	}

	log = zap.New(zapcore.NewTee(cores...), zap.AddCaller())
	return nil
}

// L mengembalikan logger global.
func L() *zap.Logger { return log }

// Sync mem-flush buffer logger (dipanggil via defer di main).
func Sync() { _ = log.Sync() }

// Debug mencatat pada level debug.
func Debug(m string, f ...zap.Field) { log.Debug(m, f...) }

// Info mencatat pada level info.
func Info(m string, f ...zap.Field) { log.Info(m, f...) }

// Warn mencatat pada level warn.
func Warn(m string, f ...zap.Field) { log.Warn(m, f...) }

// Error mencatat pada level error.
func Error(m string, f ...zap.Field) { log.Error(m, f...) }

// Fatal mencatat lalu menghentikan proses (exit 1).
func Fatal(m string, f ...zap.Field) { log.Fatal(m, f...) }
