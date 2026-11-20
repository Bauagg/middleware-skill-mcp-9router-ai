package skillindex

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

const (
	defaultMatchLimit = 5
	maxMatchLimit     = 20
)

// Controller menangani request HTTP untuk pemilihan skill.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// Match menangani GET /api/skills/match?q=...&limit=5&fallback=false:
// menampilkan skill apa yang akan terpilih untuk sebuah pesan beserta skor
// kandidatnya. fallback=true ikut menjalankan pemilih cadangan bila skor
// ambigu (memakai token), supaya hasilnya sama persis dengan request sungguhan.
func (h *Controller) Match(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
			response.FieldError{Field: "q", Message: "q wajib diisi"}))
		return
	}

	limit := defaultMatchLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxMatchLimit {
			apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
				response.FieldError{Field: "limit", Message: "limit harus angka 1-" + strconv.Itoa(maxMatchLimit)}))
			return
		}
		limit = n
	}

	withFallback := c.Query("fallback") == "true"
	response.Success(c, h.service.Match(c.Request.Context(), q, limit, withFallback), "Berhasil memilih skill")
}
