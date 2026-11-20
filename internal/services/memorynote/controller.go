package memorynote

import (
	"math"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

var validStatuses = []string{StatusProposed, StatusActive, StatusCorrected, StatusRejected, StatusDeleted}

// Controller menangani request HTTP untuk catatan memori.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// List menangani GET /api/memory-notes?status=proposed&q=...&page=1&limit=20
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
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !contains(validStatuses, status) {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
			response.FieldError{Field: "status", Message: "status harus salah satu: " + strings.Join(validStatuses, ", ")}))
		return
	}

	rows, total, err := h.service.List(ListFilter{Status: status, Query: c.Query("q"), Page: page, Limit: limit})
	if err != nil {
		apperror.Handle(c, err)
		return
	}

	response.Paginated(c, rows, response.Meta{
		Page:       page,
		Limit:      limit,
		Total:      int(total),
		TotalPages: int(math.Ceil(float64(total) / float64(limit))),
	}, "Berhasil mengambil catatan memori")
}

// Match menangani GET /api/memory-notes/match?q=... : catatan yang akan
// disisipkan untuk pesan q.
func (h *Controller) Match(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", response.FieldError{Field: "q", Message: "q wajib diisi"}))
		return
	}
	res, err := h.service.Match(c.Request.Context(), q)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "Berhasil mencocokkan catatan memori")
}

// Get menangani GET /api/memory-notes/:id.
func (h *Controller) Get(c *gin.Context) {
	row, err := h.service.Get(c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Berhasil mengambil catatan memori")
}

// Create menangani POST /api/memory-notes (catatan manual, langsung aktif).
func (h *Controller) Create(c *gin.Context) {
	var in CreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.Create(in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Created(c, row, "Catatan memori berhasil dibuat")
}

// Correct menangani PUT /api/memory-notes/:id.
func (h *Controller) Correct(c *gin.Context) {
	var in CorrectInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.Correct(c.Param("id"), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Catatan memori berhasil dikoreksi")
}

// Confirm menangani POST /api/memory-notes/:id/confirm.
func (h *Controller) Confirm(c *gin.Context) {
	row, err := h.service.Confirm(c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Catatan memori dikonfirmasi")
}

// Reject menangani POST /api/memory-notes/:id/reject.
func (h *Controller) Reject(c *gin.Context) {
	row, err := h.service.Reject(c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Catatan memori ditolak")
}

// Delete menangani DELETE /api/memory-notes/:id.
func (h *Controller) Delete(c *gin.Context) {
	if err := h.service.Delete(c.Param("id")); err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, nil, "Catatan memori berhasil dihapus")
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
