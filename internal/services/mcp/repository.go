package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	statePrefix = "skillmw:mcp:oauth:"
	stateTTL    = 10 * time.Minute
)

var serverNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// KV adalah penyimpanan sementara (Redis) untuk state login OAuth;
// clients.Redis memenuhinya.
type KV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}

// Repository mengakses file konfigurasi MCP, tabel m_mcp_credentials, dan
// state login di Redis.
type Repository struct {
	db *gorm.DB
	kv KV
}

// NewRepository membuat repository.
func NewRepository(db *gorm.DB, kv KV) *Repository {
	return &Repository{db: db, kv: kv}
}

// LoadConfig membaca file daftar server. found false bila file tidak ada
// (MCP dimatikan).
func (r *Repository) LoadConfig(path string) (cfg *FileConfig, found bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &FileConfig{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	cfg = &FileConfig{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, true, fmt.Errorf("%s bukan JSON yang valid: %w", path, err)
	}
	for name, s := range cfg.Servers {
		if !serverNameRe.MatchString(name) {
			return nil, true, fmt.Errorf("%s: nama server %q harus huruf kecil/angka/-/_ (maks 40)", path, name)
		}
		if s.URL == "" {
			return nil, true, fmt.Errorf("%s: server %q tidak punya url", path, name)
		}
	}
	return cfg, true, nil
}

// GetCredential mengambil kredensial server (nil bila belum ada).
func (r *Repository) GetCredential(ctx context.Context, server string) (*Credential, error) {
	// Find + Limit (bukan First) supaya "belum ada" tidak dicatat GORM sebagai error
	var rows []Credential
	if err := r.db.WithContext(ctx).Where("server = ?", server).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// SaveCredential menyimpan (insert atau update menurut server) kredensial.
func (r *Repository) SaveCredential(ctx context.Context, c *Credential) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "server"}}, UpdateAll: true}).
		Create(c).Error
}

// ClearTokens menghapus token login server (pendaftaran klien tetap disimpan
// supaya login berikutnya tidak perlu mendaftar ulang).
func (r *Repository) ClearTokens(ctx context.Context, server string) error {
	return r.db.WithContext(ctx).Model(&Credential{}).Where("server = ?", server).Updates(map[string]any{
		"access_token_enc": "", "refresh_token_enc": "", "token_type": "", "expires_at": nil, "connected_at": nil,
	}).Error
}

// SaveState menyimpan state login OAuth selama 10 menit.
func (r *Repository) SaveState(ctx context.Context, state string, st oauthState) error {
	b, _ := json.Marshal(st)
	return r.kv.Set(ctx, statePrefix+state, string(b), stateTTL)
}

// TakeState mengambil state login lalu menandainya terpakai, supaya link
// callback yang sama tidak bisa dipakai dua kali.
func (r *Repository) TakeState(ctx context.Context, state string) (*oauthState, error) {
	raw, found, err := r.kv.Get(ctx, statePrefix+state)
	if err != nil {
		return nil, err
	}
	if !found || raw == "used" {
		return nil, errors.New("state login tidak dikenal atau kedaluwarsa, ulangi dari /connect")
	}
	_ = r.kv.Set(ctx, statePrefix+state, "used", stateTTL)

	var st oauthState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, err
	}
	return &st, nil
}
