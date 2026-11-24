package auth

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// Controller menangani request HTTP untuk akun dan token login.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// Register menangani POST /api/auth/register.
func (h *Controller) Register(c *gin.Context) {
	var in RegisterInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	res, err := h.service.Register(c.Request.Context(), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Created(c, res, "Registrasi berhasil")
}

// Login menangani POST /api/auth/login.
func (h *Controller) Login(c *gin.Context) {
	var in LoginInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	res, err := h.service.Login(c.Request.Context(), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "Login berhasil")
}

// Refresh menangani POST /api/auth/refresh.
func (h *Controller) Refresh(c *gin.Context) {
	var in RefreshInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	res, err := h.service.Refresh(c.Request.Context(), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "Token diperbarui")
}

// Me menangani GET /api/auth/me (butuh access token login).
func (h *Controller) Me(c *gin.Context) {
	ctx := c.Request.Context()
	if !userctx.FromToken(ctx) {
		apperror.Handle(c, apperror.NewUnauthorized("Endpoint ini butuh access token login (Authorization: Bearer)"))
		return
	}
	row, err := h.service.Me(ctx, userctx.From(ctx))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Berhasil mengambil akun")
}
