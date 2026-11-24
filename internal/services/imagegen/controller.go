package imagegen

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

// Controller menangani request HTTP untuk gambar hasil generate.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// Serve menangani GET /images/:file. Nama file acak dan isinya tidak pernah
// berubah, jadi boleh di-cache lama oleh browser/aplikasi chat.
func (h *Controller) Serve(c *gin.Context) {
	p, ok := h.service.FilePath(c.Param("file"))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.File(p)
}

// List menangani GET /api/images?q=...&page=1&limit=20
func (h *Controller) List(c *gin.Context) {
	page, err := intQuery(c, "page", 1, 1, math.MaxInt32)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	limit, err := intQuery(c, "limit", defaultLimit, 1, maxLimit)
	if err != nil {
		apperror.Handle(c, err)
		return
	}

	rows, total, err := h.service.List(ListFilter{UserID: userctx.Scope(c.Request.Context()), Query: strings.TrimSpace(c.Query("q")), Page: page, Limit: limit})
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Paginated(c, rows, response.Meta{
		Page:       page,
		Limit:      limit,
		Total:      int(total),
		TotalPages: int(math.Ceil(float64(total) / float64(limit))),
	}, "Berhasil mengambil daftar gambar")
}

// Get menangani GET /api/images/:id.
func (h *Controller) Get(c *gin.Context) {
	row, err := h.service.Get(userctx.Scope(c.Request.Context()), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Berhasil mengambil gambar")
}

// intQuery membaca query param angka dengan nilai bawaan dan rentang.
func intQuery(c *gin.Context, key string, def, min, max int) (int, error) {
	raw := c.Query(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, apperror.NewBadRequest("Validasi gagal",
			response.FieldError{Field: key, Message: key + " harus angka " + strconv.Itoa(min) + "-" + strconv.Itoa(max)})
	}
	return n, nil
}
