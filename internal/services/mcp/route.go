package mcp

import "github.com/gin-gonic/gin"

// RegisterRoutes mendaftarkan endpoint MCP pada grup yang diberikan (/api/mcp).
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.GET("/servers", h.Servers)
	rg.GET("/servers/:name/connect", h.Connect)
	rg.POST("/servers/:name/refresh", h.Refresh)
	rg.DELETE("/servers/:name/connection", h.Logout)
	rg.GET("/oauth/callback", h.Callback)
	rg.GET("/tools", h.Tools)
}
