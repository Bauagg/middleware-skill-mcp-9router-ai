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

// Migrate membuang index unik lama (satu kredensial per server, dari sebelum
// login per user) yang menghalangi kredensial per (user, server).
func (r *Repository) Migrate() error {
	return r.db.Exec(`DROP INDEX IF EXISTS idx_m_mcp_credentials_server`).Error
}

// GetCredential mengambil kredensial user untuk satu server (nil bila belum ada).
func (r *Repository) GetCredential(ctx context.Context, userID, server string) (*Credential, error) {
	// Find + Limit (bukan First) supaya "belum ada" tidak dicatat GORM sebagai error
	var rows []Credential
	if err := r.db.WithContext(ctx).Where("user_id = ? AND server = ?", userID, server).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// SaveCredential menyimpan (insert atau update menurut user + server) kredensial.
func (r *Repository) SaveCredential(ctx context.Context, c *Credential) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "server"}}, UpdateAll: true}).
		Create(c).Error
}

// ClearTokens menghapus token login user (pendaftaran klien tetap disimpan
// supaya login berikutnya tidak perlu mendaftar ulang).
func (r *Repository) ClearTokens(ctx context.Context, userID, server string) error {
	return r.db.WithContext(ctx).Model(&Credential{}).Where("user_id = ? AND server = ?", userID, server).Updates(map[string]any{
		"access_token_enc": "", "refresh_token_enc": "", "token_type": "", "expires_at": nil, "connected_at": nil,
	}).Error
}

// DeleteCredential menghapus kredensial user untuk satu server.
func (r *Repository) DeleteCredential(ctx context.Context, userID, server string) error {
	return r.db.WithContext(ctx).Where("user_id = ? AND server = ?", userID, server).Delete(&Credential{}).Error
}

// ListUserServers mengambil server MCP milik user, terurut menurut nama.
func (r *Repository) ListUserServers(ctx context.Context, userID string) ([]UserServer, error) {
	var rows []UserServer
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("name").Find(&rows).Error
	return rows, err
}

// AllUserServers mengambil server MCP tambahan milik semua user (untuk admin).
func (r *Repository) AllUserServers(ctx context.Context) ([]UserServer, error) {
	var rows []UserServer
	err := r.db.WithContext(ctx).Order("user_id, name").Find(&rows).Error
	return rows, err
}

// AllConnections mengambil status login semua user ke semua server (untuk
// admin). Hanya kolom ringkas yang dibaca: secret dan token tidak ikut.
func (r *Repository) AllConnections(ctx context.Context) ([]Connection, error) {
	var rows []Connection
	err := r.db.WithContext(ctx).Model(&Credential{}).
		Select("user_id, server, access_token_enc <> '' AS logged_in, expires_at, connected_at").
		Order("user_id, server").Scan(&rows).Error
	return rows, err
}

// CreateUserServer menyimpan server MCP milik user.
func (r *Repository) CreateUserServer(ctx context.Context, row *UserServer) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// DeleteUserServer menghapus server MCP milik user.
func (r *Repository) DeleteUserServer(ctx context.Context, userID, name string) (int64, error) {
	res := r.db.WithContext(ctx).Where("user_id = ? AND name = ?", userID, name).Delete(&UserServer{})
	return res.RowsAffected, res.Error
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
