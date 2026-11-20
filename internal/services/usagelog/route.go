package usagelog

import "github.com/gin-gonic/gin"

// RegisterRoutes mendaftarkan endpoint log pemakaian pada grup yang diberikan.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.GET("", h.List)
	rg.GET("/summary", h.Summary)
}
