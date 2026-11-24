package websearch

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

// Controller menangani request HTTP untuk web search.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// Search menangani GET /api/web-search?q=... : untuk mengecek koneksi ke
// SearXNG tanpa lewat chat.
func (h *Controller) Search(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", response.FieldError{Field: "q", Message: "q wajib diisi"}))
		return
	}
	res, err := h.service.Search(c.Request.Context(), q)
	if err != nil {
		apperror.Handle(c, apperror.NewBadRequest(err.Error()))
		return
	}
	response.Success(c, res, "Hasil pencarian web")
}
