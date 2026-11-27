package aiaccount

import (
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
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

// Controller menangani request HTTP provider, akun AI, dan video.
type Controller struct {
	service *Service
}

// NewController membuat controller.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// ListDrivers menangani GET /api/ai/drivers: jenis layanan AI yang didukung.
func (h *Controller) ListDrivers(c *gin.Context) {
	response.Success(c, h.service.Drivers(), "Berhasil mengambil driver AI")
}

// ListProviders menangani GET /api/ai/providers.
func (h *Controller) ListProviders(c *gin.Context) {
	rows, err := h.service.Providers(c.Request.Context())
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, rows, "Berhasil mengambil provider AI")
}

// CreateProvider menangani POST /api/ai/providers (admin).
func (h *Controller) CreateProvider(c *gin.Context) {
	var in CreateProviderInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Data tidak valid", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.CreateProvider(c.Request.Context(), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Created(c, row, "Provider AI ditambahkan")
}

// UpdateProvider menangani PATCH /api/ai/providers/:id (admin).
func (h *Controller) UpdateProvider(c *gin.Context) {
	var in UpdateProviderInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Data tidak valid", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.UpdateProvider(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Provider AI diperbarui")
}

// DeleteProvider menangani DELETE /api/ai/providers/:id (admin).
func (h *Controller) DeleteProvider(c *gin.Context) {
	if err := h.service.DeleteProvider(c.Request.Context(), c.Param("id")); err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, nil, "Provider AI dihapus")
}

// ListAccounts menangani GET /api/ai/accounts.
func (h *Controller) ListAccounts(c *gin.Context) {
	rows, err := h.service.Accounts(c.Request.Context())
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, rows, "Berhasil mengambil akun AI")
}

// CreateAccount menangani POST /api/ai/accounts.
func (h *Controller) CreateAccount(c *gin.Context) {
	var in CreateAccountInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Data tidak valid", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.CreateAccount(c.Request.Context(), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Created(c, row, "Akun AI ditambahkan")
}

// UpdateAccount menangani PATCH /api/ai/accounts/:id.
func (h *Controller) UpdateAccount(c *gin.Context) {
	var in UpdateAccountInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Data tidak valid", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.UpdateAccount(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Akun AI diperbarui")
}

// DeleteAccount menangani DELETE /api/ai/accounts/:id.
func (h *Controller) DeleteAccount(c *gin.Context) {
	if err := h.service.DeleteAccount(c.Request.Context(), c.Param("id")); err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, nil, "Akun AI dihapus")
}

// CheckAccount menangani POST /api/ai/accounts/:id/check.
func (h *Controller) CheckAccount(c *gin.Context) {
	row, err := h.service.CheckAccount(c.Request.Context(), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Akun AI dicek")
}

// ResetCooldown menangani POST /api/ai/accounts/:id/reset-cooldown.
func (h *Controller) ResetCooldown(c *gin.Context) {
	row, err := h.service.ResetCooldown(c.Request.Context(), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Jeda limit akun direset")
}

// LoginStatus menangani GET /api/ai/login: apakah login lewat browser tersedia.
func (h *Controller) LoginStatus(c *gin.Context) {
	response.Success(c, gin.H{"available": h.service.LoginAvailable(c.Request.Context())}, "Berhasil")
}

// StartLogin menangani POST /api/ai/login.
func (h *Controller) StartLogin(c *gin.Context) {
	var in StartLoginInput
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&in); err != nil {
			apperror.Handle(c, apperror.NewBadRequest("Data tidak valid", apperror.BindingErrors(err)...))
			return
		}
	}
	v, err := h.service.StartLogin(c.Request.Context(), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Created(c, v, "Jendela login dibuka")
}

// GetLogin menangani GET /api/ai/login/:id: keadaan login (FE memantaunya
// sampai akun tersimpan otomatis).
func (h *Controller) GetLogin(c *gin.Context) {
	v, err := h.service.LoginStatus(c.Request.Context(), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, v, "Berhasil")
}

// FinishLogin menangani POST /api/ai/login/:id/finish.
func (h *Controller) FinishLogin(c *gin.Context) {
	var in FinishLoginInput
	if err := c.ShouldBindJSON(&in); err != nil {
		apperror.Handle(c, apperror.NewBadRequest("Data tidak valid", apperror.BindingErrors(err)...))
		return
	}
	row, err := h.service.FinishLogin(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Sesi login tersimpan")
}

// CancelLogin menangani DELETE /api/ai/login/:id.
func (h *Controller) CancelLogin(c *gin.Context) {
	if err := h.service.CancelLogin(c.Request.Context(), c.Param("id")); err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, nil, "Login dibatalkan")
}

// LoginVNC menangani GET /api/ai/login/vnc?ticket=...: WebSocket layar
// browser login, diteruskan ke websockify. Browser tidak bisa mengirim header
// token di WebSocket, jadi aksesnya lewat tiket sekali pakai dari StartLogin.
func (h *Controller) LoginVNC(c *gin.Context) {
	addr, ok := h.service.VNCAddr(c.Request.Context(), c.Query("ticket"))
	if !ok {
		apperror.Handle(c, apperror.NewForbidden("tiket layar login tidak berlaku: mulai login lagi"))
		return
	}
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.URL.Path, r.Out.URL.RawQuery = "/", ""
		r.Out.Header.Del("Cookie")
		r.Out.Header.Del("Authorization")
	}}
	proxy.ServeHTTP(c.Writer, c.Request)
}

// ServeVideo menangani GET /videos/:file. Nama file acak dan isinya tidak
// pernah berubah, jadi boleh di-cache lama.
func (h *Controller) ServeVideo(c *gin.Context) {
	p, ok := h.service.VideoPath(c.Param("file"))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.File(p) // mendukung Range, jadi video bisa diputar sambil diunduh
}

// ListVideos menangani GET /api/videos?q=...&page=1&limit=20
func (h *Controller) ListVideos(c *gin.Context) {
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
	rows, total, err := h.service.Videos(VideoFilter{
		UserID: userctx.Scope(c.Request.Context()), Query: strings.TrimSpace(c.Query("q")), Page: page, Limit: limit,
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
	}, "Berhasil mengambil daftar video")
}

// GetVideo menangani GET /api/videos/:id.
func (h *Controller) GetVideo(c *gin.Context) {
	row, err := h.service.Video(userctx.Scope(c.Request.Context()), c.Param("id"))
	if err != nil {
		apperror.Handle(c, err)
		return
	}
	response.Success(c, row, "Berhasil mengambil video")
}

// intQuery membaca query param angka dengan nilai bawaan dan rentang.
func intQuery(c *gin.Context, key string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		return 0, apperror.NewBadRequest(key + " harus angka " + strconv.Itoa(min) + "-" + strconv.Itoa(max))
	}
	return v, nil
}
