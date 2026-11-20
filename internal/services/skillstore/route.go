package skillstore

import "github.com/gin-gonic/gin"

// RegisterRoutes mendaftarkan endpoint skill pada grup yang diberikan.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.GET("", h.List)
	rg.GET("/preview/:name", h.Preview)
	rg.POST("/upload", h.Upload)
}
