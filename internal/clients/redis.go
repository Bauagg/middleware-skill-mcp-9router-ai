package clients

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
)

// warnEvery membatasi log peringatan Redis supaya tidak membanjiri log saat
// Redis mati (satu peringatan per jangka waktu ini).
const warnEvery = 30 * time.Second

// Redis adalah klien Redis untuk cache. Semua operasi memakai timeout pendek:
// cache hanya optimasi, jadi Redis yang lambat atau mati tidak boleh
// memperlambat request (pemanggil memperlakukan error sebagai cache miss).
type Redis struct {
	client   *redis.Client
	lastWarn atomic.Int64
}

// NewRedis membuat klien Redis. Koneksi dibuat saat dipakai, jadi pembuatan
// klien tidak gagal walau Redis sedang mati.
// timeout adalah batas tiap operasi (baca/tulis); batas membuka koneksi
// minimal 500 ms.
func NewRedis(addr, password string, db int, timeout time.Duration) *Redis {
	return &Redis{client: redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  max(500*time.Millisecond, 2*timeout),
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
		MaxRetries:   -1, // tanpa retry: gagal cepat lebih baik daripada menunda request
	})}
}

// Ping memeriksa koneksi ke Redis.
func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Get membaca nilai key. found false (tanpa error) bila key tidak ada.
func (r *Redis) Get(ctx context.Context, key string) (value string, found bool, err error) {
	value, err = r.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		r.warn(err)
		return "", false, err
	}
	return value, true, nil
}

// Set menyimpan nilai dengan masa berlaku ttl.
func (r *Redis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		r.warn(err)
		return err
	}
	return nil
}

// Close menutup koneksi.
func (r *Redis) Close() error {
	return r.client.Close()
}

// warn mencatat error Redis, paling sering sekali per warnEvery.
func (r *Redis) warn(err error) {
	now := time.Now().UnixNano()
	last := r.lastWarn.Load()
	if now-last < int64(warnEvery) || !r.lastWarn.CompareAndSwap(last, now) {
		return
	}
	logger.Warn("redis error, cache dilewati (request tetap berjalan)", zap.Error(err))
}
