package aiaccount

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/uuidparam"
)

// RegisterRoutes mendaftarkan:
//   - GET /videos/:file di root (di luar /api), supaya link video di jawaban
//     chat pendek dan bisa dibuka langsung;
//   - /api/videos untuk daftar video per user;
//   - /api/ai/drivers dan /api/ai/providers: layanan AI yang bisa dihubungkan
//     (provider hanya diubah admin);
//   - /api/ai/accounts dan /api/ai/login: setiap user menghubungkan akun AI-nya
//     sendiri (admin melihat dan mengelola akun semua user).
func RegisterRoutes(r *gin.Engine, api *gin.RouterGroup, h *Controller) {
	r.GET("/videos/:file", h.ServeVideo)

	user := userctx.Require()
	admin := userctx.RequireAdmin()
	id := uuidparam.Require("id")

	v := api.Group("/videos", user)
	v.GET("", h.ListVideos)
	v.GET("/:id", id, h.GetVideo)

	ai := api.Group("/ai")
	ai.GET("/drivers", user, h.ListDrivers)

	p := ai.Group("/providers", user)
	p.GET("", h.ListProviders)
	p.POST("", admin, h.CreateProvider)
	p.PATCH("/:id", admin, id, h.UpdateProvider)
	p.DELETE("/:id", admin, id, h.DeleteProvider)

	// layar browser login: dibuka lewat tiket dari POST /login (tanpa token)
	ai.GET("/login/vnc", h.LoginVNC)
	l := ai.Group("/login", user)
	l.GET("", h.LoginStatus)
	l.POST("", h.StartLogin)
	l.GET("/:id", h.GetLogin)
	l.POST("/:id/finish", h.FinishLogin)
	l.DELETE("/:id", h.CancelLogin)

	a := ai.Group("/accounts", user)
	a.GET("", h.ListAccounts)
	a.POST("", h.CreateAccount)
	a.PATCH("/:id", id, h.UpdateAccount)
	a.DELETE("/:id", id, h.DeleteAccount)
	a.POST("/:id/check", id, h.CheckAccount)
	a.POST("/:id/reset-cooldown", id, h.ResetCooldown)
}
