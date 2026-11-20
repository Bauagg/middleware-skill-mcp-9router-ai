package proxy

import "github.com/gin-gonic/gin"

// RegisterRoutes mendaftarkan proxy untuk semua method di bawah /v1/*.
func RegisterRoutes(r *gin.Engine, h *Controller) {
	r.Any("/v1/*path", h.Handle)
}
