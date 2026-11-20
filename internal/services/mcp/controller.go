package mcp

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

// Controller menangani request HTTP untuk MCP.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// Servers menangani GET /api/mcp/servers.
func (h *Controller) Servers(c *gin.Context) {
	response.Success(c, h.service.Statuses(c.Request.Context()), "Berhasil mengambil server MCP")
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
	response.Success(c, h.service.Tools(c.Query("q"), limit), "Berhasil mengambil tool MCP")
}

// Connect menangani GET /api/mcp/servers/:name/connect: mengarahkan browser ke
// halaman login server. Dengan ?redirect=false, alamatnya dikembalikan sebagai JSON.
func (h *Controller) Connect(c *gin.Context) {
	authURL, err := h.service.StartLogin(c.Request.Context(), c.Param("name"))
	if err != nil {
		apperror.Handle(c, err)
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
	callbackPage(c, http.StatusOK, "Login berhasil", detail+" Tab ini boleh ditutup.")
}

// Refresh menangani POST /api/mcp/servers/:name/refresh.
func (h *Controller) Refresh(c *gin.Context) {
	st, err := h.service.Refresh(c.Request.Context(), c.Param("name"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, st, "Daftar tool diambil ulang")
}

// Logout menangani DELETE /api/mcp/servers/:name/connection.
func (h *Controller) Logout(c *gin.Context) {
	st, err := h.service.Logout(c.Request.Context(), c.Param("name"))
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
