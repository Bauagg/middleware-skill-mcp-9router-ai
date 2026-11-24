package websearch

import "github.com/gin-gonic/gin"

// RegisterRoutes mendaftarkan endpoint web search pada grup yang diberikan (/api/web-search).
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.GET("", h.Search)
}
