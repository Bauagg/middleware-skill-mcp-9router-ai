package middleware

import (
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORS mengembalikan middleware CORS (padanan `app.use(cors())` di Express).
// Untuk produksi, persempit AllowOrigins ke domain yang diizinkan.
func CORS() gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		// X-API-Key (secret key client), X-User-Id (identitas user), dan
		// X-Conversation-Id harus diizinkan supaya aplikasi web bisa mengirimnya
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-API-Key", "X-Router-Key", "X-Router-URL", "X-User-Id", "X-Conversation-Id"},
		ExposeHeaders:    []string{"Content-Length", "X-Skill", "X-MCP-Tools"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	})
}
