package mcp

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// RegisterRoutes mendaftarkan endpoint MCP pada grup yang diberikan (/api/mcp).
// Endpoint data butuh header X-User-Id. /connect juga menerima link
// bertanda tangan (dibuka browser), dan /oauth/callback membawa user di state.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	user := userctx.Require()

	rg.GET("/servers", user, h.Servers)
	rg.POST("/servers", user, h.AddServer)
	rg.DELETE("/servers/:name", user, h.RemoveServer)
	rg.POST("/servers/:name/refresh", user, h.Refresh)
	rg.DELETE("/servers/:name/connection", user, h.Logout)
	rg.GET("/tools", user, h.Tools)

	// admin (pemilik aplikasi) melihat server dan login MCP semua user
	rg.GET("/admin/servers", user, userctx.RequireAdmin(), h.AllUsers)

	rg.GET("/servers/:name/connect", h.Connect)
	rg.GET("/oauth/callback", h.Callback)
}
