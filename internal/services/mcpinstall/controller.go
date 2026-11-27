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
	// koneksi database: user menjelaskan dengan kata-kata sendiri, middleware
	// menyusun alamat koneksinya (DSN). Bila belum lengkap, form ditampilkan
	// lagi beserta teksnya (halaman ini tidak di-cache).
	values, texts := map[string]string{}, map[string]string{}
	var problems, summaries []string
	for _, k := range v.EnvKeys {
		val := strings.TrimSpace(c.PostForm(k))
		texts[k] = val // ditampilkan lagi bila ada isian lain yang belum lengkap
		if val == "" || !isConnKey(k) {
			values[k] = val
			continue
		}
		conn, summary, err := buildConn(k, val)
		if err != nil {
			problems = append(problems, v.EnvGuide[k].Label+": "+err.Error())
			continue
		}
		values[k] = conn
		summaries = append(summaries, summary)
	}
	if len(problems) > 0 {
		d := formData(v, c.Request.URL.RawQuery, "")
		d.Error = strings.Join(problems, ". ") + ". Lengkapi penjelasannya lalu simpan lagi."
		for i := range d.Fields {
			d.Fields[i].Text = texts[d.Fields[i].Key]
		}
		envPage(c, http.StatusBadRequest, d)
		return
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
	if len(summaries) > 0 {
		note = "Database: " + strings.Join(summaries, "; ") + ". " + note
	}
	envPage(c, http.StatusOK, formData(v, c.Request.URL.RawQuery, note))
}

type envField struct {
	Key, Label, Help, Example string
	Set                       bool
	// Multiline: isian panjang (sertifikat, kubeconfig, penjelasan database):
	// kotak lebih tinggi dan tombol unggah file
	Multiline bool
	Text      string // isian yang ditampilkan lagi saat belum lengkap
}

type envData struct {
	Title, Detail, Note, Error, Action, Name, Repo string
	Fields                                         []envField
}

func formData(v *View, rawQuery, note string) envData {
	d := envData{Title: "Data akses MCP " + v.Name, Name: v.Name, Repo: v.RepoURL, Note: note, Action: "?" + rawQuery}
	set := map[string]bool{}
	for _, k := range v.EnvSet {
		set[k] = true
	}
	for _, k := range v.EnvKeys {
		g := v.EnvGuide[k]
		d.Fields = append(d.Fields, envField{Key: k, Label: g.Label, Help: g.Help, Example: g.Example, Set: set[k], Multiline: g.Multiline})
	}
	return d
}

var envTmpl = template.Must(template.New("env").Parse(`<!doctype html>
<html lang="id"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer"><title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:640px;margin:40px auto;padding:0 16px;color:#222;line-height:1.45}
.field{margin:22px 0}label{display:block;margin-bottom:4px;font-weight:600}
textarea{width:100%;padding:9px;box-sizing:border-box;font:inherit;border:1px solid #bbb;border-radius:6px;resize:vertical}
textarea.multi{min-height:130px;font-family:ui-monospace,monospace;font-size:.9em}textarea::placeholder{color:#999}
.help{color:#555;font-size:.92em;margin:0 0 6px}.upload{font-size:.9em;margin-top:6px}
button{margin-top:20px;padding:10px 20px;font:inherit;border-radius:6px;border:0;background:#5b3df5;color:#fff;cursor:pointer}
small{color:#666}.note{background:#eef7ee;padding:10px 12px;border-radius:6px}.error{background:#fdecec;color:#8a1f1f;padding:10px 12px;border-radius:6px}</style></head>
<body><h2>{{.Title}}</h2>
{{if .Detail}}<p>{{.Detail}}</p>{{end}}
{{if .Note}}<p class="note">{{.Note}}</p>{{end}}
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
{{if .Fields}}<p><small>Isi dengan kata-katamu sendiri mengikuti contoh samar di dalam kotak. Disimpan terenkripsi dan tidak pernah
ditampilkan lagi. Kosongkan isian yang tidak ingin diubah.</small></p>
<form method="post" action="{{.Action}}" autocomplete="off">
{{range .Fields}}<div class="field"><label for="{{.Key}}">{{.Label}}{{if .Set}} <small>(sudah diisi)</small>{{end}}</label>
{{if .Help}}<p class="help">{{.Help}}</p>{{end}}
<textarea id="{{.Key}}" name="{{.Key}}" spellcheck="false" autocomplete="off" rows="{{if .Multiline}}6{{else}}2{{end}}"{{if .Multiline}} class="multi"{{end}} placeholder="{{.Example}}">{{.Text}}</textarea>
{{if .Multiline}}<div class="upload">atau unggah file: <input type="file" data-target="{{.Key}}"></div>{{end}}</div>
{{end}}<button type="submit">Simpan</button></form>
<script>
document.querySelectorAll('input[type=file][data-target]').forEach(function (inp) {
  inp.addEventListener('change', function () {
    var f = inp.files[0]; if (!f) return;
    if (f.size > 65536) { alert('File lebih dari 64 KB'); inp.value = ''; return; }
    var r = new FileReader();
    r.onload = function () { document.getElementById(inp.dataset.target).value = r.result; };
    r.readAsText(f);
  });
});
</script>{{end}}
</body></html>`))

func envPage(c *gin.Context, status int, d envData) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Frame-Options", "DENY")
	c.Status(status)
	_ = envTmpl.Execute(c.Writer, d)
}
