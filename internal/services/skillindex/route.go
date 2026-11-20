package skillindex

import "github.com/gin-gonic/gin"

// RegisterRoutes mendaftarkan endpoint pemilihan skill pada grup yang diberikan.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.GET("/match", h.Match)
}
