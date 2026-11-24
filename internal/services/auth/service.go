// Package auth adalah register, login, dan refresh token untuk user yang
// mendaftar langsung di middleware (web middleware AI). Ini pengganti bagi
// user yang tidak datang dari aplikasi pihak ketiga: aplikasi pihak ketiga
// tetap memakai X-API-Key + X-User-Id, sedangkan user yang login memakai
// header Authorization: Bearer <access_token> dan user_id-nya = id di m_users.
package auth

import (
	"context"
	"errors"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/jwtauth"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// pesan login gagal sengaja sama untuk email tidak ada dan password salah,
// supaya tidak bisa dipakai menebak email mana yang terdaftar
const loginFailed = "Email atau password salah"

// Service mengelola akun dan token login.
type Service struct {
	repo   *Repository
	tokens *jwtauth.Issuer
	// dummyHash dibandingkan saat email tidak ada, supaya lama respons login
	// sama dengan saat password salah (tidak membocorkan email terdaftar)
	dummyHash []byte
}

// NewService membuat Service.
func NewService(repo *Repository, tokens *jwtauth.Issuer) *Service {
	dummy, _ := bcrypt.GenerateFromPassword([]byte("password-pembanding-saja"), bcrypt.DefaultCost)
	return &Service{repo: repo, tokens: tokens, dummyHash: dummy}
}

// Register membuat akun baru (selalu role user) lalu langsung login.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*AuthResult, error) {
	email := normalizeEmail(in.Email)
	if _, err := s.repo.ByEmail(ctx, email); err == nil {
		return nil, apperror.NewConflict("Email sudah terdaftar")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	// role selalu user: field role di body diabaikan. Admin dibuat langsung di
	// database (UPDATE m_users SET role = 'admin') atau dari ADMIN_EMAIL di .env
	row := &User{Email: email, Password: string(hash), Role: userctx.RoleUser}
	if err := s.repo.Create(ctx, row); err != nil {
		return nil, err // email ganda (balapan) dipetakan apperror.Handle menjadi 409
	}
	logger.Info("user terdaftar", zap.String("user_id", row.ID))
	return s.issue(row)
}

// Login memeriksa email dan password lalu mengembalikan token.
func (s *Service) Login(ctx context.Context, in LoginInput) (*AuthResult, error) {
	row, err := s.repo.ByEmail(ctx, normalizeEmail(in.Email))
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(in.Password))
		return nil, apperror.NewUnauthorized(loginFailed)
	}
	if bcrypt.CompareHashAndPassword([]byte(row.Password), []byte(in.Password)) != nil {
		return nil, apperror.NewUnauthorized(loginFailed)
	}
	return s.issue(row)
}

// Refresh menukar refresh token dengan pasangan token baru. Role dibaca ulang
// dari database, jadi perubahan role langsung berlaku di token berikutnya, dan
// akun yang sudah dihapus tidak bisa refresh lagi.
func (s *Service) Refresh(ctx context.Context, in RefreshInput) (*AuthResult, error) {
	claims, err := s.tokens.Parse(strings.TrimSpace(in.RefreshToken), jwtauth.TypeRefresh)
	if err != nil {
		return nil, apperror.NewUnauthorized("Refresh token tidak valid atau kedaluwarsa")
	}
	row, err := s.repo.ByID(ctx, claims.Subject)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.NewUnauthorized("Akun tidak ditemukan")
		}
		return nil, err
	}
	return s.issue(row)
}

// Me mengambil akun user yang sedang login.
func (s *Service) Me(ctx context.Context, id string) (*User, error) {
	row, err := s.repo.ByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NewNotFound("Akun tidak ditemukan")
	}
	return row, err
}

// EnsureAdmin dipanggil saat start: membuat akun admin (pemilik aplikasi)
// dari ADMIN_EMAIL/ADMIN_PASSWORD bila belum ada. Akun yang sudah ada dengan
// email itu hanya dinaikkan menjadi admin bila password-nya sama, supaya orang
// yang lebih dulu mendaftar memakai email admin tidak otomatis jadi admin.
func (s *Service) EnsureAdmin(ctx context.Context, email, password string) error {
	email = normalizeEmail(email)
	if email == "" || password == "" {
		return nil
	}
	row, err := s.repo.ByEmail(ctx, email)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if err := s.repo.Create(ctx, &User{Email: email, Password: string(hash), Role: userctx.RoleAdmin}); err != nil {
			return err
		}
		logger.Info("akun admin dibuat", zap.String("email", email))
		return nil
	}
	if err != nil {
		return err
	}
	if row.Role == userctx.RoleAdmin {
		return nil
	}
	if bcrypt.CompareHashAndPassword([]byte(row.Password), []byte(password)) != nil {
		logger.Warn("ADMIN_EMAIL sudah dipakai akun lain dengan password berbeda: tidak dijadikan admin",
			zap.String("email", email))
		return nil
	}
	logger.Info("akun dinaikkan menjadi admin", zap.String("email", email))
	return s.repo.SetRole(ctx, row.ID, userctx.RoleAdmin)
}

func (s *Service) issue(row *User) (*AuthResult, error) {
	pair, err := s.tokens.Issue(row.ID, row.Role)
	if err != nil {
		return nil, err
	}
	return &AuthResult{User: row, Pair: pair}, nil
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
