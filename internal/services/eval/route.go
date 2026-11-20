package eval

import "github.com/gin-gonic/gin"

// RegisterRoutes mendaftarkan endpoint eval pada grup yang diberikan.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.POST("/runs", h.Run)
	rg.GET("/runs", h.List)
	rg.GET("/runs/:id", h.Get)
}
