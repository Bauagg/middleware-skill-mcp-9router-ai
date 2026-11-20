package eval

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/uuidparam"
)

const (
	defaultRunsLimit = 20
	maxRunsLimit     = 100
)

// Controller menangani request HTTP untuk eval.
type Controller struct {
	service *Service
}

// NewController membuat controller eval.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// Run menangani POST /api/evals/runs. Eval berjalan di latar belakang;
// respons berisi run_id untuk dipantau lewat GET /api/evals/runs/:id.
func (h *Controller) Run(c *gin.Context) {
	var in RunInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Format request tidak valid",
			response.FieldError{Field: "body", Message: "kirim JSON: {\"skill\": \"nama-skill\", \"variants\": [\"full\",\"short\",\"none\"]}"}))
		return
	}

	started, err := h.service.Start(in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}

	response.Accepted(c, started, "Eval dimulai, pantau hasilnya lewat GET /api/evals/runs/"+started.RunID)
}

// Get menangani GET /api/evals/runs/:id?detail=true.
func (h *Controller) Get(c *gin.Context) {
	id := c.Param("id")
	if !uuidparam.Valid(id) {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", response.FieldError{Field: "id", Message: "run_id harus UUID"}))
		return
	}

	sum, err := h.service.Summary(id, c.Query("detail") == "true")
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, sum, "Berhasil mengambil hasil eval")
}

// List menangani GET /api/evals/runs?skill=...&limit=20.
func (h *Controller) List(c *gin.Context) {
	limit := defaultRunsLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxRunsLimit {
			apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
				response.FieldError{Field: "limit", Message: "limit harus angka 1-" + strconv.Itoa(maxRunsLimit)}))
			return
		}
		limit = n
	}

	runs, err := h.service.Runs(strings.TrimSpace(c.Query("skill")), limit)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, runs, "Berhasil mengambil daftar eval")
}
