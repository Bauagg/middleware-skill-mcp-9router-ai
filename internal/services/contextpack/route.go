package contextpack

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/uuidparam"
)

// RegisterRoutes mendaftarkan endpoint context pack pada grup yang diberikan.
// Semua endpoint butuh header X-User-Id: data context pack milik user itu saja.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.Use(userctx.Require())
	rg.GET("", h.List)
	rg.POST("", h.Create)
	rg.GET("/:id", uuidparam.Require("id"), h.Get)
	rg.PUT("/:id", uuidparam.Require("id"), h.Update)
	rg.DELETE("/:id", uuidparam.Require("id"), h.Delete)
}
