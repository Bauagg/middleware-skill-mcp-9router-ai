package imagegen

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/uuidparam"
)

// RegisterRoutes mendaftarkan:
//   - GET /images/:file di root (di luar /api), supaya link gambar di jawaban
//     chat pendek dan bisa dibuka langsung;
//   - GET /api/images dan /api/images/:id untuk daftar catatan gambar.
func RegisterRoutes(r *gin.Engine, api *gin.RouterGroup, h *Controller) {
	r.GET("/images/:file", h.Serve)

	// daftar gambar per user (header X-User-Id); file /images/:file tetap
	// terbuka karena link-nya dibuka langsung oleh aplikasi chat
	g := api.Group("/images", userctx.Require())
	g.GET("", h.List)
	g.GET("/:id", uuidparam.Require("id"), h.Get)
}
