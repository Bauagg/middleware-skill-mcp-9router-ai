package auth

import "github.com/gin-gonic/gin"

// RegisterRoutes mendaftarkan endpoint akun. register, login, dan refresh
// terbuka tanpa X-API-Key (dipanggil web middleware AI dari browser); daftar
// endpoint terbuka ada di middleware.Auth.
func RegisterRoutes(rg *gin.RouterGroup, h *Controller) {
	rg.POST("/register", h.Register)
	rg.POST("/login", h.Login)
	rg.POST("/refresh", h.Refresh)
	rg.GET("/me", h.Me)
}
