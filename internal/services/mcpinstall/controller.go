package mcpinstall

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// Controller menangani request HTTP pemasangan MCP dari GitHub.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

func me(c *gin.Context) string    { return userctx.From(c.Request.Context()) }
func scope(c *gin.Context) string { return userctx.Scope(c.Request.Context()) }

// Analyze menangani POST /api/mcp/installs/analyze.
func (h *Controller) Analyze(c *gin.Context) {
	var in AnalyzeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	res, err := h.service.Analyze(c.Request.Context(), in.RepoURL)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "Repo dianalisis (belum ada kode yang dijalankan)")
}

// Submit menangani POST /api/mcp/installs.
func (h *Controller) Submit(c *gin.Context) {
	var in SubmitInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	res, err := h.service.Submit(c.Request.Context(), me(c), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Created(c, res, "Pemasangan MCP diajukan")
}

// List menangani GET /api/mcp/installs?status=.
func (h *Controller) List(c *gin.Context) {
	rows, err := h.service.List(c.Request.Context(), scope(c), me(c), strings.TrimSpace(c.Query("status")))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, rows, "Berhasil mengambil pemasangan MCP")
}

// Get menangani GET /api/mcp/installs/:id.
func (h *Controller) Get(c *gin.Context) {
	res, err := h.service.Get(c.Request.Context(), scope(c), me(c), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "Berhasil mengambil pemasangan MCP")
}

// Approve menangani POST /api/mcp/installs/:id/approve (khusus admin).
func (h *Controller) Approve(c *gin.Context) {
	res, err := h.service.Approve(c.Request.Context(), me(c), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "Pemasangan MCP disetujui")
}

// Reject menangani POST /api/mcp/installs/:id/reject (khusus admin).
func (h *Controller) Reject(c *gin.Context) {
	var in ReviewInput
	_ = c.ShouldBindJSON(&in)
	res, err := h.service.Reject(c.Request.Context(), me(c), c.Param("id"), in.Reason)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "Pemasangan MCP ditolak")
}

// Start menangani POST /api/mcp/installs/:id/start.
func (h *Controller) Start(c *gin.Context) {
	res, err := h.service.Start(c.Request.Context(), scope(c), me(c), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "MCP sedang dijalankan")
}

// Stop menangani POST /api/mcp/installs/:id/stop.
func (h *Controller) Stop(c *gin.Context) {
	res, err := h.service.Stop(c.Request.Context(), scope(c), me(c), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "MCP dihentikan")
}

// Remove menangani DELETE /api/mcp/installs/:id.
func (h *Controller) Remove(c *gin.Context) {
	if err := h.service.Remove(c.Request.Context(), scope(c), me(c), c.Param("id")); err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, nil, "Pemasangan MCP dihapus")
}

// Logs menangani GET /api/mcp/installs/:id/logs.
func (h *Controller) Logs(c *gin.Context) {
	out, err := h.service.Logs(c.Request.Context(), scope(c), me(c), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, gin.H{"logs": out}, "Berhasil mengambil log")
}

// envUser mengenali pengisi form: dari link bertanda tangan (dibuka browser,
// header tidak dipercaya), atau dari token/X-User-Id bila dipanggil aplikasi.
func (h *Controller) envUser(c *gin.Context) string {
	if c.Query("sig") != "" {
		if u := c.Query("u"); h.service.VerifyEnvLink(c.Param("id"), u, c.Query("exp"), c.Query("sig")) {
			return u
		}
		return ""
	}
	if u := userctx.Resolve(c); userctx.Valid(u) {
		return u
	}
	return ""
}

// EnvForm menangani GET /api/mcp/installs/:id/env: halaman form data rahasia.
func (h *Controller) EnvForm(c *gin.Context) {
	user := h.envUser(c)
	if user == "" {
		envPage(c, http.StatusBadRequest, envData{Title: "Link tidak valid", Detail: "Link sudah kedaluwarsa atau salah; minta link baru lewat chat."})
		return
	}
	v, err := h.service.EnvForm(c.Request.Context(), user, c.Param("id"))
	if err != nil {
		envPage(c, http.StatusNotFound, envData{Title: "Tidak ditemukan", Detail: "Pemasangan MCP tidak ditemukan untuk akun ini."})
		return
	}
	envPage(c, http.StatusOK, formData(v, c.Request.URL.RawQuery, ""))
}

// SaveEnv menangani POST /api/mcp/installs/:id/env (form urlencoded).
func (h *Controller) SaveEnv(c *gin.Context) {
	user := h.envUser(c)
	if user == "" {
		envPage(c, http.StatusBadRequest, envData{Title: "Link tidak valid", Detail: "Link sudah kedaluwarsa atau salah; minta link baru lewat chat."})
		return
	}
	v, err := h.service.EnvForm(c.Request.Context(), user, c.Param("id"))
	if err != nil {
		envPage(c, http.StatusNotFound, envData{Title: "Tidak ditemukan", Detail: "Pemasangan MCP tidak ditemukan untuk akun ini."})
		return
	}
	values := map[string]string{}
	for _, k := range v.EnvKeys {
		values[k] = c.PostForm(k)
	}
	v, err = h.service.SetEnv(c.Request.Context(), user, c.Param("id"), values)
	if err != nil {
		envPage(c, http.StatusBadRequest, envData{Title: "Gagal menyimpan", Detail: err.Error()})
		return
	}
	note := "Tersimpan (terenkripsi). Kembali ke chat."
	switch {
	case len(v.EnvMissing) > 0:
		note = "Tersimpan. Masih ada yang belum diisi."
	case v.Status == StatusPendingReview:
		note = "Tersimpan. Server dijalankan setelah admin menyetujui. Kembali ke chat."
	case v.ReviewedAt != nil || v.Mine != nil:
		note = "Tersimpan. Server sedang dijalankan (bisa beberapa menit). Kembali ke chat."
	}
	envPage(c, http.StatusOK, formData(v, c.Request.URL.RawQuery, note))
}

type envField struct {
	Key string
	Set bool
}

type envData struct {
	Title, Detail, Note, Action, Name, Repo string
	Fields                                  []envField
}

func formData(v *View, rawQuery, note string) envData {
	d := envData{Title: "Data rahasia MCP " + v.Name, Name: v.Name, Repo: v.RepoURL, Note: note, Action: "?" + rawQuery}
	set := map[string]bool{}
	for _, k := range v.EnvSet {
		set[k] = true
	}
	for _, k := range v.EnvKeys {
		d.Fields = append(d.Fields, envField{Key: k, Set: set[k]})
	}
	return d
}

var envTmpl = template.Must(template.New("env").Parse(`<!doctype html>
<html lang="id"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer"><title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:560px;margin:40px auto;padding:0 16px;color:#222}
label{display:block;margin:14px 0 4px;font-weight:600}input{width:100%;padding:8px;box-sizing:border-box;font-family:monospace}
button{margin-top:18px;padding:10px 18px}small{color:#666}.note{background:#eef7ee;padding:10px;border-radius:6px}</style></head>
<body><h2>{{.Title}}</h2>
{{if .Detail}}<p>{{.Detail}}</p>{{end}}
{{if .Note}}<p class="note">{{.Note}}</p>{{end}}
{{if .Fields}}<p><small>Repo: {{.Repo}}. Nilai disimpan terenkripsi dan tidak pernah ditampilkan lagi. Kosongkan kolom yang tidak ingin diubah.</small></p>
<form method="post" action="{{.Action}}" autocomplete="off">
{{range .Fields}}<label for="{{.Key}}">{{.Key}}{{if .Set}} <small>(sudah diisi)</small>{{end}}</label>
<input type="password" id="{{.Key}}" name="{{.Key}}" autocomplete="new-password">
{{end}}<button type="submit">Simpan</button></form>{{end}}
</body></html>`))

func envPage(c *gin.Context, status int, d envData) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Frame-Options", "DENY")
	c.Status(status)
	_ = envTmpl.Execute(c.Writer, d)
}
