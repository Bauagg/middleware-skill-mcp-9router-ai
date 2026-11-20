package skillstore

import (
	"archive/zip"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/modelbudget"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

// Controller menangani request HTTP untuk skill.
type Controller struct {
	service *Service
	budgets modelbudget.Budgets
}

// NewController membuat controller skill. budgets dipakai oleh preview untuk
// menentukan batas token sebuah model.
func NewController(service *Service, budgets modelbudget.Budgets) *Controller {
	return &Controller{service: service, budgets: budgets}
}

// Preview menangani GET /api/skills/preview/:name?model=...&budget=...:
// menampilkan teks skill persis seperti yang akan disisipkan ke prompt
// (sudah dibersihkan dan disesuaikan dengan budget), tanpa memanggil model.
// budget (angka, 0 = tanpa batas) mengalahkan model; tanpa keduanya dipakai
// budget bawaan.
func (h *Controller) Preview(c *gin.Context) {
	name := c.Param("name")
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
			response.FieldError{Field: "name", Message: "nama skill tidak valid"}))
		return
	}

	budget := h.budgets.For(c.Query("model"))
	if raw := c.Query("budget"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
				response.FieldError{Field: "budget", Message: "budget harus angka >= 0"}))
			return
		}
		budget = n
	}

	rendered, err := h.service.Render(name, budget)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			apperror.Handle(c, apperror.NewNotFound("Skill tidak ditemukan"))
			return
		}
		logger.Error("preview skill gagal", zap.String("skill", name), zap.Error(err))
		apperror.Handle(c, apperror.NewInternalServerError("Gagal menyiapkan skill"))
		return
	}

	response.Success(c, PreviewResult{Name: name, Budget: budget, Rendered: *rendered}, "Berhasil menyiapkan preview skill")
}

// List menangani GET /api/skills: daftar skill yang terpasang.
func (h *Controller) List(c *gin.Context) {
	skills, err := h.service.List()
	if err != nil {
		apperror.Handle(c, err)
		return
	}

	response.Success(c, skills, "Berhasil mengambil daftar skill")
}

// Upload menangani POST /api/skills/upload (multipart/form-data):
//   - file      : zip berisi satu atau beberapa skill (wajib, .zip atau .skill)
//   - overwrite : "true" untuk mengganti skill yang namanya sudah ada (opsional)
func (h *Controller) Upload(c *gin.Context) {
	// batasi body sebelum diparse; sisa 1 MB untuk field form lain
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxZipBytes+(1<<20))

	fh, err := c.FormFile("file")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
				fieldErr("file", "file zip wajib diisi (multipart field 'file')")))
			return
		}
		apperror.Handle(c, apperror.NewBadRequest("Gagal membaca upload",
			fieldErr("file", "kirim sebagai multipart/form-data dengan ukuran maksimal 10 MB")))
		return
	}

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if ext != ".zip" && ext != ".skill" {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
			fieldErr("file", "format file harus .zip atau .skill")))
		return
	}
	if fh.Size > MaxZipBytes {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
			fieldErr("file", "ukuran file zip maksimal 10 MB")))
		return
	}

	overwrite, err := strconv.ParseBool(c.DefaultPostForm("overwrite", "false"))
	if err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
			fieldErr("overwrite", "overwrite harus true atau false")))
		return
	}

	f, err := fh.Open()
	if err != nil {
		apperror.Handle(c, internalErr("open upload", err))
		return
	}
	defer f.Close()

	zr, err := zip.NewReader(f, fh.Size)
	if err != nil {
		apperror.Handle(c, apperror.NewBadRequest("File zip tidak valid",
			fieldErr("file", "file bukan zip yang valid")))
		return
	}

	result, err := h.service.Upload(zr, UploadInput{Overwrite: overwrite})
	if err != nil {
		apperror.Handle(c, err)
		return
	}

	response.Created(c, result, "Skill berhasil diupload")
}
