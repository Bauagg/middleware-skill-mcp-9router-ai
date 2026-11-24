package mcp

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// Controller menangani request HTTP untuk MCP.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

func userOf(c *gin.Context) string { return userctx.From(c.Request.Context()) }

// Servers menangani GET /api/mcp/servers: server bawaan + milik user.
func (h *Controller) Servers(c *gin.Context) {
	response.Success(c, h.service.Statuses(c.Request.Context(), userOf(c)), "Berhasil mengambil server MCP")
}

// AllUsers menangani GET /api/mcp/admin/servers (khusus admin): server MCP
// tambahan dan status login semua user.
func (h *Controller) AllUsers(c *gin.Context) {
	res, err := h.service.AllUsers(c.Request.Context())
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, res, "Berhasil mengambil server MCP semua user")
}

// AddServer menangani POST /api/mcp/servers: tambah server MCP milik user.
func (h *Controller) AddServer(c *gin.Context) {
	var in AddServerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", apperror.BindingErrors(err)...))
		return
	}
	st, note, err := h.service.AddServer(c.Request.Context(), userOf(c), in.Name, in.URL, in.Keywords, in.SharedLogin)
	if err != nil {
		apperror.Handle(c, apperror.NewBadRequest(err.Error()))
		return
	}
	msg := "Server MCP ditambahkan"
	if note != "" {
		msg += ": " + note
	}
	response.Created(c, st, msg)
}

// RemoveServer menangani DELETE /api/mcp/servers/:name: hapus server milik user.
func (h *Controller) RemoveServer(c *gin.Context) {
	if err := h.service.RemoveServer(c.Request.Context(), userOf(c), c.Param("name")); err != nil {
		apperror.Handle(c, apperror.NewBadRequest(err.Error()))
		return
	}
	response.Success(c, nil, "Server MCP dihapus")
}

// Tools menangani GET /api/mcp/tools?q=...&limit=...
func (h *Controller) Tools(c *gin.Context) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			apperror.Handle(c, apperror.NewBadRequest("Validasi gagal", response.FieldError{Field: "limit", Message: "limit harus angka >= 1"}))
			return
		}
		limit = n
	}
	response.Success(c, h.service.Tools(c.Request.Context(), userOf(c), c.Query("q"), limit), "Berhasil mengambil tool MCP")
}

// Connect menangani GET /api/mcp/servers/:name/connect: mengarahkan browser ke
// halaman login server. User dikenali dari header X-User-Id, atau dari link
// bertanda tangan (u, exp, sig) yang dibagikan chat, karena browser tidak bisa
// mengirim header. Dengan ?redirect=false, alamatnya dikembalikan sebagai JSON.
func (h *Controller) Connect(c *gin.Context) {
	name := c.Param("name")
	// link bertanda tangan dibuka tanpa X-API-Key/token, jadi header X-User-Id
	// tidak boleh dipercaya di jalur ini: user hanya dari tanda tangan link
	var user string
	if c.Query("sig") != "" {
		if u := c.Query("u"); h.service.VerifyLink(u, name, c.Query("exp"), c.Query("sig")) {
			user = u
		}
	} else if u := userctx.Resolve(c); userctx.Valid(u) {
		user = u
	}
	if user == "" {
		msg := "Link login tidak valid atau sudah kedaluwarsa; minta link baru lewat chat."
		if c.Query("redirect") == "false" {
			apperror.Handle(c, apperror.NewBadRequest(msg))
		} else {
			callbackPage(c, http.StatusBadRequest, "Login tidak bisa dimulai", msg)
		}
		return
	}

	authURL, err := h.service.StartLogin(c.Request.Context(), user, name)
	if err != nil {
		if c.Query("redirect") == "false" {
			apperror.Handle(c, err)
			return
		}
		msg := err.Error()
		if ae, ok := err.(*apperror.AppError); ok {
			msg = ae.Message
		}
		callbackPage(c, http.StatusBadRequest, "Login tidak bisa dimulai", msg)
		return
	}
	if c.Query("redirect") == "false" {
		response.Success(c, gin.H{"auth_url": authURL}, "Buka auth_url di browser untuk login")
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

// Callback menangani GET /api/mcp/oauth/callback (dipanggil browser setelah
// login). Balasannya halaman HTML singkat.
func (h *Controller) Callback(c *gin.Context) {
	if e := c.Query("error"); e != "" {
		callbackPage(c, http.StatusBadRequest, "Login dibatalkan atau ditolak", e+" "+c.Query("error_description"))
		return
	}
	state, code := c.Query("state"), c.Query("code")
	if state == "" || code == "" {
		callbackPage(c, http.StatusBadRequest, "Login gagal", "parameter state atau code tidak ada")
		return
	}

	st, err := h.service.FinishLogin(c.Request.Context(), state, code)
	if err != nil {
		msg := err.Error()
		if ae, ok := err.(*apperror.AppError); ok {
			msg = ae.Message
		}
		callbackPage(c, http.StatusBadRequest, "Login gagal", msg)
		return
	}

	detail := st.Name + " terhubung: " + strconv.Itoa(st.Tools) + " tool tersedia."
	if st.State != StateConnected {
		detail = st.Name + " sudah login, tetapi daftar tool belum bisa diambil (" + st.State + " " + st.Error + ")."
	}
	callbackPage(c, http.StatusOK, "Login berhasil", detail+" Tab ini boleh ditutup lalu kembali ke chat.")
}

// Refresh menangani POST /api/mcp/servers/:name/refresh.
func (h *Controller) Refresh(c *gin.Context) {
	st, err := h.service.Refresh(c.Request.Context(), userOf(c), c.Param("name"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, st, "Daftar tool diambil ulang")
}

// Logout menangani DELETE /api/mcp/servers/:name/connection.
func (h *Controller) Logout(c *gin.Context) {
	st, err := h.service.Logout(c.Request.Context(), userOf(c), c.Param("name"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, st, "Login server MCP dihapus")
}

var pageTmpl = template.Must(template.New("p").Parse(`<!doctype html><html lang="id"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:560px;margin:15vh auto;padding:0 16px;line-height:1.5}
h1{font-size:1.4rem}</style></head><body><h1>{{.Title}}</h1><p>{{.Detail}}</p></body></html>`))

func callbackPage(c *gin.Context, status int, title, detail string) {
	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = pageTmpl.Execute(c.Writer, map[string]string{"Title": title, "Detail": strings.TrimSpace(detail)})
}
