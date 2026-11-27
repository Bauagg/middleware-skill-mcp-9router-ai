package aiaccount

import (
	"context"
	"errors"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// Drivers mengembalikan driver yang tersedia, untuk form tambah provider.
func (s *Service) Drivers() []DriverInfo {
	out := make([]DriverInfo, 0, len(s.order))
	for _, k := range s.order {
		out = append(out, s.drivers[k].Info())
	}
	return out
}

// Seed membuat provider bawaan (satu per driver Builtin) yang belum ada.
func (s *Service) Seed(ctx context.Context) error {
	rows, err := s.repo.Providers(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, p := range rows {
		have[p.Driver] = true
	}
	next := len(rows)
	for _, k := range s.order {
		info := s.drivers[k].Info()
		if !info.Builtin || have[k] {
			continue
		}
		p := &Provider{Driver: k, Name: info.Name, Enabled: true, Priority: next}
		if err := s.repo.CreateProvider(ctx, p); err != nil {
			return err
		}
		next++
		logger.Info("provider AI bawaan ditambahkan", zap.String("driver", k))
	}
	return nil
}

// Providers mengambil provider beserta info driver-nya. User biasa hanya
// melihat provider yang aktif; admin melihat semua.
func (s *Service) Providers(ctx context.Context) ([]Provider, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	rows, err := s.providers(ctx)
	if err != nil {
		return nil, err
	}
	if userctx.IsAdmin(ctx) {
		return rows, nil
	}
	out := rows[:0]
	for _, p := range rows {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out, nil
}

// providers mengambil semua provider yang driver-nya dikenal, urut prioritas.
func (s *Service) providers(ctx context.Context) ([]Provider, error) {
	rows, err := s.repo.Providers(ctx)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, p := range rows {
		if d, ok := s.drivers[p.Driver]; ok {
			p.Info = d.Info()
			out = append(out, p)
		}
	}
	return out, nil
}

// provider mengambil satu provider beserta info driver-nya.
func (s *Service) provider(ctx context.Context, id string) (*Provider, Driver, error) {
	p, err := s.repo.Provider(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, apperror.NewNotFound("provider tidak ditemukan")
	}
	if err != nil {
		return nil, nil, err
	}
	d, ok := s.drivers[p.Driver]
	if !ok {
		return nil, nil, apperror.NewBadRequest("driver provider " + p.Driver + " tidak dikenal")
	}
	p.Info = d.Info()
	return p, d, nil
}

// CreateProvider menambah provider (admin). Driver bawaan sudah punya
// providernya sendiri; yang ditambah admin biasanya API kompatibel OpenAI.
func (s *Service) CreateProvider(ctx context.Context, in CreateProviderInput) (*Provider, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	d, ok := s.drivers[in.Driver]
	if !ok {
		return nil, apperror.NewBadRequest("driver " + in.Driver + " tidak dikenal")
	}
	info := d.Info()
	if info.Builtin {
		return nil, apperror.NewBadRequest(info.Name + " sudah tersedia sebagai provider bawaan")
	}
	p := &Provider{
		Driver: in.Driver, Name: strings.TrimSpace(in.Name), Enabled: true,
		LoginURL: strings.TrimSpace(in.LoginURL), BaseURL: strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"),
		ChatModel: strings.TrimSpace(in.ChatModel), ImageModel: strings.TrimSpace(in.ImageModel),
	}
	if err := validateProvider(p, info); err != nil {
		return nil, err
	}
	rows, err := s.repo.Providers(ctx)
	if err != nil {
		return nil, err
	}
	p.Priority = len(rows)
	if err := s.repo.CreateProvider(ctx, p); err != nil {
		return nil, err
	}
	p.Info = info
	logger.Info("provider AI ditambahkan", zap.String("driver", p.Driver), zap.String("name", p.Name))
	return p, nil
}

// UpdateProvider mengubah provider (admin). Sesi akun di dalamnya dibuat
// ulang karena alamat/model bisa berubah.
func (s *Service) UpdateProvider(ctx context.Context, id string, in UpdateProviderInput) (*Provider, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	p, _, err := s.provider(ctx, id)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n != "" {
			fields["name"], p.Name = n, n
		}
	}
	if in.Enabled != nil {
		fields["enabled"] = *in.Enabled
	}
	if in.Priority != nil {
		fields["priority"] = *in.Priority
	}
	if in.LoginURL != nil {
		p.LoginURL = strings.TrimSpace(*in.LoginURL)
		fields["login_url"] = p.LoginURL
	}
	if in.BaseURL != nil {
		p.BaseURL = strings.TrimRight(strings.TrimSpace(*in.BaseURL), "/")
		fields["base_url"] = p.BaseURL
	}
	if in.ChatModel != nil {
		p.ChatModel = strings.TrimSpace(*in.ChatModel)
		fields["chat_model"] = p.ChatModel
	}
	if in.ImageModel != nil {
		p.ImageModel = strings.TrimSpace(*in.ImageModel)
		fields["image_model"] = p.ImageModel
	}
	if err := validateProvider(p, p.Info); err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		if err := s.repo.UpdateProvider(ctx, id, fields); err != nil {
			return nil, err
		}
	}
	s.dropProviderSessions(id)
	p, _, err = s.provider(ctx, id)
	return p, err
}

// DeleteProvider menghapus provider tambahan beserta akun semua user di
// dalamnya (admin). Provider bawaan hanya bisa dimatikan.
func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	p, _, err := s.provider(ctx, id)
	if err != nil {
		return err
	}
	if p.Info.Builtin {
		return apperror.NewBadRequest("provider bawaan tidak bisa dihapus; matikan saja bila tidak dipakai")
	}
	if err := s.repo.DeleteProvider(ctx, id); err != nil {
		return err
	}
	s.dropProviderSessions(id)
	logger.Info("provider AI dihapus", zap.String("name", p.Name))
	return nil
}

func validateProvider(p *Provider, info DriverInfo) error {
	if p.Name == "" {
		return apperror.NewBadRequest("nama provider wajib diisi")
	}
	if p.LoginURL != "" && !isHTTPURL(p.LoginURL) {
		return apperror.NewBadRequest("alamat login harus diawali http:// atau https://")
	}
	if info.NeedsBaseURL && !isHTTPURL(p.BaseURL) {
		return apperror.NewBadRequest("alamat API wajib diisi, diawali http:// atau https:// (mis. https://api.openai.com/v1)")
	}
	if info.ModelFields && p.ChatModel == "" && p.ImageModel == "" {
		return apperror.NewBadRequest("isi model chat dan/atau model gambar (mis. gpt-4o-mini, gpt-image-1)")
	}
	return nil
}

// supports melaporkan apakah provider dipakai untuk jenis ini: driver-nya
// mampu, dan (untuk driver API) modelnya diisi admin.
func (p *Provider) supports(kind string) bool {
	switch kind {
	case kindText:
		return p.Info.Text && (!p.Info.ModelFields || p.ChatModel != "")
	case kindImage:
		return p.Info.Image && (!p.Info.ModelFields || p.ImageModel != "")
	case kindVideo:
		return p.Info.Video
	}
	return false
}

func isHTTPURL(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}
