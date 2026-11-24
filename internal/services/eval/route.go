package eval

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// RegisterRoutes mendaftarkan endpoint eval pada grup yang diberikan.
// Semua butuh identitas user: user biasa hanya melihat eval yang ia
// jalankan, admin melihat semua.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.Use(userctx.Require())
	rg.POST("/runs", h.Run)
	rg.GET("/runs", h.List)
	rg.GET("/runs/:id", h.Get)
}
