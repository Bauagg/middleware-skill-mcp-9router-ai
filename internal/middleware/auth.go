package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/jwtauth"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// APIKeyHeader adalah header yang membawa secret key aplikasi pihak ketiga.
const APIKeyHeader = "X-API-Key"

// Auth mengenali siapa pemanggil. Ada dua cara masuk:
//
//  1. Token login (user yang register/login di middleware, mis. web
//     middleware AI): header Authorization: Bearer <access_token>. user_id dan
//     role diambil dari token; header X-User-Id diabaikan.
//  2. Aplikasi pihak ketiga: header X-API-Key yang cocok dengan API_KEYS, dan
//     user-nya dikirim di header X-User-Id. Selalu dianggap role user.
//
// Endpoint yang dibuka langsung oleh browser (health check, file gambar, alur
// login MCP) dan register/login/refresh tidak butuh keduanya.
func Auth(keys []string, tokens *jwtauth.Issuer) gin.HandlerFunc {
	// key dibandingkan dalam bentuk hash berukuran tetap, supaya perbandingan
	// constant-time tidak membocorkan panjang key
	hashes := make([][32]byte, 0, len(keys))
	for _, k := range keys {
		hashes = append(hashes, sha256.Sum256([]byte(k)))
	}
	validKey := func(got string) bool {
		if got == "" {
			return false
		}
		sum := sha256.Sum256([]byte(got))
		ok := 0
		for _, h := range hashes {
			ok |= subtle.ConstantTimeCompare(sum[:], h[:])
		}
		return ok == 1
	}

	return func(c *gin.Context) {
		if public(c) {
			c.Next()
			return
		}

		// SDK OpenAI selalu mengirim "Authorization: Bearer <api_key>", jadi
		// hanya isi yang berbentuk JWT yang diperlakukan sebagai token login
		bearer := bearerToken(c)
		if jwtauth.LooksLikeJWT(bearer) {
			claims, err := tokens.Parse(bearer, jwtauth.TypeAccess)
			if err == nil {
				ctx := userctx.WithToken(userctx.WithRole(userctx.With(c.Request.Context(), claims.Subject), claims.Role))
				c.Request = c.Request.WithContext(ctx)
				c.Next()
				return
			}
			// token login rusak/kedaluwarsa: tetap boleh lewat bila membawa X-API-Key
			if !validKey(strings.TrimSpace(c.GetHeader(APIKeyHeader))) {
				deny(c, "access token tidak valid atau kedaluwarsa, perbarui lewat /api/auth/refresh", "invalid_token")
				return
			}
		}

		if validKey(strings.TrimSpace(c.GetHeader(APIKeyHeader))) {
			// pihak ketiga selalu role user; user_id dibaca dari X-User-Id nanti
			c.Request = c.Request.WithContext(userctx.WithRole(c.Request.Context(), userctx.RoleUser))
			c.Next()
			return
		}

		deny(c, "butuh Authorization: Bearer <access_token> (login) atau header "+APIKeyHeader, "invalid_api_key")
	}
}

// bearerToken mengambil token dari header Authorization: Bearer.
func bearerToken(c *gin.Context) string {
	h := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// deny menjawab 401: format error OpenAI untuk /v1/* supaya terbaca SDK.
func deny(c *gin.Context, msg, typ string) {
	if strings.HasPrefix(c.Request.URL.Path, "/v1/") {
		c.Data(http.StatusUnauthorized, "application/json", clients.OpenAIErrorBody(msg, typ))
	} else {
		response.Unauthorized(c, msg, nil)
	}
	c.Abort()
}

// public melaporkan endpoint yang tidak butuh token maupun X-API-Key.
func public(c *gin.Context) bool {
	p := c.Request.URL.Path
	switch {
	case p == "/" || p == "/health":
		return true
	case strings.HasPrefix(p, "/images/"):
		return true // link gambar di jawaban chat (nama file acak)
	case p == "/api/mcp/oauth/callback":
		return true // dipanggil browser setelah login; user dibawa di state login
	case strings.HasPrefix(p, "/api/mcp/servers/") && strings.HasSuffix(p, "/connect") && c.Query("sig") != "":
		return true // link login bertanda tangan dari chat (diverifikasi di controller)
	case strings.HasPrefix(p, "/api/mcp/installs/") && strings.HasSuffix(p, "/env") && c.Query("sig") != "":
		return true // form data rahasia MCP dari link bertanda tangan (diverifikasi di controller)
	case c.Request.Method == http.MethodPost &&
		(p == "/api/auth/register" || p == "/api/auth/login" || p == "/api/auth/refresh"):
		return true // web middleware AI mendaftar/login dari browser, belum punya token
	}
	return false
}
