// Package jwtauth membuat dan memeriksa token login (JWT HS256) untuk user
// yang mendaftar langsung di middleware (web middleware AI). Ada dua jenis:
//   - access token: umur pendek, dikirim di header Authorization: Bearer;
//   - refresh token: umur panjang, hanya untuk meminta pasangan token baru.
//
// Jenis token ditulis di klaim "typ" supaya refresh token tidak bisa dipakai
// sebagai access token, dan sebaliknya.
package jwtauth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Jenis token.
const (
	TypeAccess  = "access"
	TypeRefresh = "refresh"
)

// MinSecretLen adalah panjang minimal JWT_SECRET (HS256 butuh kunci >= 256 bit).
const MinSecretLen = 32

// Claims adalah isi token: sub = user_id, role = user | admin.
type Claims struct {
	Role string `json:"role"`
	Type string `json:"typ"`
	jwt.RegisteredClaims
}

// Pair adalah pasangan token yang dikembalikan saat register, login, dan refresh.
type Pair struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`         // detik, umur access token
	RefreshExpiresIn int    `json:"refresh_expires_in"` // detik, umur refresh token
}

// Issuer membuat dan memeriksa token.
type Issuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewIssuer membuat Issuer. secret wajib >= MinSecretLen karakter.
func NewIssuer(secret string, accessTTL, refreshTTL time.Duration) (*Issuer, error) {
	if len(secret) < MinSecretLen {
		return nil, errors.New("JWT_SECRET minimal 32 karakter")
	}
	return &Issuer{secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL}, nil
}

// Issue membuat pasangan access + refresh token untuk satu user.
func (i *Issuer) Issue(userID, role string) (*Pair, error) {
	access, err := i.sign(userID, role, TypeAccess, i.accessTTL)
	if err != nil {
		return nil, err
	}
	refresh, err := i.sign(userID, role, TypeRefresh, i.refreshTTL)
	if err != nil {
		return nil, err
	}
	return &Pair{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		ExpiresIn: int(i.accessTTL.Seconds()), RefreshExpiresIn: int(i.refreshTTL.Seconds()),
	}, nil
}

func (i *Issuer) sign(userID, role, typ string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Role: role,
		Type: typ,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
}

// Parse memeriksa tanda tangan, masa berlaku, dan jenis token. Hanya HS256
// yang diterima, supaya token dengan alg lain (mis. "none") ditolak.
func (i *Issuer) Parse(token, wantType string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, err
	}
	if claims.Type != wantType || claims.Subject == "" {
		return nil, errors.New("jenis token salah")
	}
	return claims, nil
}

// LooksLikeJWT melaporkan apakah string berbentuk JWT (tiga bagian dipisah
// titik). Dipakai untuk membedakan token login dari isi Authorization lain
// (mis. SDK OpenAI selalu mengirim "Bearer <api_key>").
func LooksLikeJWT(s string) bool {
	dots := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			dots++
		}
	}
	return dots == 2 && len(s) > 20
}
