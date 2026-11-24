package mcpinstall

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/uuidparam"
)

// RegisterRoutes mendaftarkan endpoint pemasangan MCP dari GitHub
// (/api/mcp/installs). User biasa hanya melihat pengajuannya sendiri; admin
// melihat semua dan yang menyetujui. Form data rahasia (/env) juga bisa dibuka
// lewat link bertanda tangan dari chat (browser tidak mengirim header).
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	user := userctx.Require()
	id := uuidparam.Require("id")

	rg.POST("/analyze", user, h.Analyze)
	rg.POST("", user, h.Submit)
	rg.GET("", user, h.List)
	rg.GET("/:id", user, id, h.Get)
	rg.POST("/:id/approve", user, userctx.RequireAdmin(), id, h.Approve)
	rg.POST("/:id/reject", user, userctx.RequireAdmin(), id, h.Reject)
	rg.POST("/:id/start", user, id, h.Start)
	rg.POST("/:id/stop", user, id, h.Stop)
	rg.DELETE("/:id", user, id, h.Remove)
	rg.GET("/:id/logs", user, id, h.Logs)

	rg.GET("/:id/env", id, h.EnvForm)
	rg.POST("/:id/env", id, h.SaveEnv)
}
