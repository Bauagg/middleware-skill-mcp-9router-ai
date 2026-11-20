package usagelog

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

const (
	defaultLimit = 10
	maxLimit     = 100
)

// Controller menangani request HTTP untuk log pemakaian.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// List menangani GET /api/usage-logs?page=1&limit=10&model=...&skill=...
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

	rows, total, err := h.service.List(ListFilter{
		Model: strings.TrimSpace(c.Query("model")),
		Skill: strings.TrimSpace(c.Query("skill")),
		Page:  page,
		Limit: limit,
	})
	if err != nil {
		apperror.Handle(c, err)
		return
	}

	response.Paginated(c, rows, response.Meta{
		Page:       page,
		Limit:      limit,
		Total:      int(total),
		TotalPages: int(math.Ceil(float64(total) / float64(limit))),
	}, "Berhasil mengambil log pemakaian")
}

// Summary menangani GET /api/usage-logs/summary?from=...&to=...
// from/to berformat RFC3339 atau YYYY-MM-DD; to tidak ikut dihitung.
func (h *Controller) Summary(c *gin.Context) {
	var f SummaryFilter
	var err error

	if f.From, err = timeQuery(c, "from"); err != nil {
		apperror.Handle(c, err)
		return
	}
	if f.To, err = timeQuery(c, "to"); err != nil {
		apperror.Handle(c, err)
		return
	}

	result, err := h.service.Summary(f)
	if err != nil {
		apperror.Handle(c, err)
		return
	}

	response.Success(c, result, "Berhasil meringkas pemakaian")
}

// intQuery membaca query param angka dengan batas min..max.
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

// timeQuery membaca query param waktu (RFC3339 atau YYYY-MM-DD); nil bila kosong.
func timeQuery(c *gin.Context, key string) (*time.Time, error) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return nil, nil
	}

	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t, nil
		}
	}
	return nil, apperror.NewBadRequest("Validasi gagal",
		response.FieldError{Field: key, Message: key + " harus berformat RFC3339 atau YYYY-MM-DD"})
}
