package memorynote

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/uuidparam"
)

// RegisterRoutes mendaftarkan endpoint catatan memori pada grup yang diberikan.
// Semua endpoint butuh header X-User-Id: hanya catatan milik user itu.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.Use(userctx.Require())
	id := uuidparam.Require("id")

	rg.GET("", h.List)
	rg.POST("", h.Create)
	rg.GET("/match", h.Match)
	rg.GET("/:id", id, h.Get)
	rg.PUT("/:id", id, h.Correct)
	rg.DELETE("/:id", id, h.Delete)
	rg.POST("/:id/confirm", id, h.Confirm)
	rg.POST("/:id/reject", id, h.Reject)
}
