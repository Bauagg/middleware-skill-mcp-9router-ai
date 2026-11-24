package contextpack

import (
	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// Controller menangani request HTTP untuk context pack.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// List menangani GET /api/context-packs.
func (h *Controller) List(c *gin.Context) {
	rows, err := h.service.List(userctx.Scope(c.Request.Context()))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, rows, "Berhasil mengambil context pack")
}

// Get menangani GET /api/context-packs/:id.
func (h *Controller) Get(c *gin.Context) {
	row, err := h.service.Get(userctx.Scope(c.Request.Context()), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Berhasil mengambil context pack")
}

// Create menangani POST /api/context-packs.
func (h *Controller) Create(c *gin.Context) {
	var in CreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.Create(userctx.From(c.Request.Context()), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Created(c, row, "Context pack berhasil dibuat")
}

// Update menangani PUT /api/context-packs/:id.
func (h *Controller) Update(c *gin.Context) {
	var in UpdateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.Update(userctx.Scope(c.Request.Context()), c.Param("id"), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Context pack berhasil diubah")
}

// Delete menangani DELETE /api/context-packs/:id.
func (h *Controller) Delete(c *gin.Context) {
	if err := h.service.Delete(userctx.Scope(c.Request.Context()), c.Param("id")); err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, nil, "Context pack berhasil dihapus")
}
