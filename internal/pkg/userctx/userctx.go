// Package userctx membawa identitas user (header X-User-Id) di sepanjang
// request. Middleware tidak punya daftar user sendiri: user_id dikirim
// aplikasi client (mis. Luma) dan dipercaya apa adanya, lalu dipakai untuk
// memisahkan data per user (log, memori, gambar, koneksi MCP).
package userctx

import (
	"context"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

// Role user. Admin adalah pemilik aplikasi: daftar data (context pack,
// catatan, gambar, log, eval, server MCP) tidak difilter per user. User
// biasa hanya melihat datanya sendiri. Skill selalu global untuk semua.
// User dari aplikasi pihak ketiga (X-API-Key + X-User-Id) selalu RoleUser.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// noneScope tidak pernah cocok dengan user_id mana pun (tidak lolos Valid),
// dipakai bila user biasa entah bagaimana tidak punya id.
const noneScope = "!"

// Header adalah nama header yang membawa user_id.
const Header = "X-User-Id"

// RouterKeyHeader membawa key 9Router milik user. Middleware hanya alat di
// depan 9Router: setiap user memakai key (dan model) 9Router miliknya sendiri.
const RouterKeyHeader = "X-Router-Key"

// RouterURLHeader membawa alamat dasar 9Router milik user (mis.
// https://host/v1). Setiap user punya domain 9Router sendiri, jadi alamatnya
// tidak ditetapkan di .env.
const RouterURLHeader = "X-Router-URL"

// idRe membatasi user_id ke karakter aman (dipakai di key cache dan link).
var idRe = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,128}$`)

type ctxKey struct{}

type roleCtx struct{}

type tokenCtx struct{}

type routerKeyCtx struct{}

type routerModelCtx struct{}

type routerURLCtx struct{}

// Valid melaporkan apakah user_id berformat benar.
func Valid(id string) bool { return idRe.MatchString(id) }

// FromHeader membaca dan merapikan user_id dari header request.
func FromHeader(c *gin.Context) string { return strings.TrimSpace(c.GetHeader(Header)) }

// With menyimpan user_id di context.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From mengambil user_id dari context ("" bila tidak ada).
func From(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// RouterKeyFromHeader membaca key 9Router dari header request.
func RouterKeyFromHeader(c *gin.Context) string {
	return strings.TrimSpace(c.GetHeader(RouterKeyHeader))
}

// WithRouterKey menyimpan key 9Router user di context, supaya semua panggilan
// ke 9Router selama request ini (chat, tool, gambar, pemilih cadangan,
// ekstraksi memori) memakai key user itu.
func WithRouterKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, routerKeyCtx{}, key)
}

// RouterKey mengambil key 9Router user dari context ("" bila tidak ada).
func RouterKey(ctx context.Context) string {
	k, _ := ctx.Value(routerKeyCtx{}).(string)
	return k
}

// RouterURLFromHeader membaca alamat 9Router dari header request.
func RouterURLFromHeader(c *gin.Context) string {
	return strings.TrimSpace(c.GetHeader(RouterURLHeader))
}

// WithRouterURL menyimpan alamat dasar 9Router user (sudah divalidasi) di context.
func WithRouterURL(ctx context.Context, base string) context.Context {
	return context.WithValue(ctx, routerURLCtx{}, base)
}

// RouterURL mengambil alamat dasar 9Router user dari context ("" bila tidak ada).
func RouterURL(ctx context.Context) string {
	u, _ := ctx.Value(routerURLCtx{}).(string)
	return u
}

// WithRouterModel menyimpan model 9Router yang dipilih user (field "model"
// di chat). Panggilan turunan (pemilih cadangan, ekstraksi memori) memakai
// model yang sama, jadi tidak ada model yang ditulis mati di .env.
func WithRouterModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, routerModelCtx{}, model)
}

// RouterModel mengambil model 9Router user dari context ("" bila tidak ada).
func RouterModel(ctx context.Context) string {
	m, _ := ctx.Value(routerModelCtx{}).(string)
	return m
}

// WithRole menyimpan role user di context.
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleCtx{}, role)
}

// Role mengambil role user dari context (bawaan RoleUser).
func Role(ctx context.Context) string {
	if r, _ := ctx.Value(roleCtx{}).(string); r != "" {
		return r
	}
	return RoleUser
}

// WithToken menandai bahwa identitas request berasal dari token login
// (bukan dari header X-User-Id kiriman pihak ketiga).
func WithToken(ctx context.Context) context.Context {
	return context.WithValue(ctx, tokenCtx{}, true)
}

// FromToken melaporkan apakah identitas request berasal dari token login.
func FromToken(ctx context.Context) bool {
	ok, _ := ctx.Value(tokenCtx{}).(bool)
	return ok
}

// IsAdmin melaporkan apakah user di context adalah admin.
func IsAdmin(ctx context.Context) bool { return Role(ctx) == RoleAdmin }

// Scope mengembalikan user_id untuk memfilter daftar data: "" untuk admin
// (semua data), user_id sendiri untuk user biasa. Hanya untuk endpoint
// pengelolaan /api/*, bukan untuk sisipan chat (chat selalu memakai data
// user itu sendiri, termasuk admin).
func Scope(ctx context.Context) string {
	if IsAdmin(ctx) {
		return ""
	}
	if id := From(ctx); id != "" {
		return id
	}
	return noneScope
}

// Resolve mengambil user_id request: dari token login bila ada (sudah
// dimasukkan ke context oleh middleware Auth, header diabaikan), selain itu
// dari header X-User-Id kiriman aplikasi pihak ketiga.
func Resolve(c *gin.Context) string {
	if id := From(c.Request.Context()); id != "" {
		return id
	}
	return FromHeader(c)
}

// RequireAdmin menolak (403) request dari user yang bukan admin.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsAdmin(c.Request.Context()) {
			apperror.Handle(c, apperror.NewForbidden("Hanya admin yang boleh mengakses ini"))
			c.Abort()
			return
		}
		c.Next()
	}
}

// Require adalah middleware Gin untuk endpoint /api/* yang datanya per user:
// request tanpa header X-User-Id yang valid ditolak 400, dan user_id
// dimasukkan ke context request.
func Require() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := Resolve(c)
		if !Valid(id) {
			apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", response.FieldError{
				Field: Header, Message: "header " + Header + " wajib diisi (huruf, angka, . _ @ : -, maks 128)",
			}))
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(With(c.Request.Context(), id))
		c.Next()
	}
}
