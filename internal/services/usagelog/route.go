package usagelog

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// RegisterRoutes mendaftarkan endpoint log pemakaian pada grup yang diberikan.
// User biasa hanya melihat log miliknya; admin melihat semua (filter ?user_id=).
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.Use(userctx.Require())
	rg.GET("", h.List)
	rg.GET("/summary", h.Summary)
}
