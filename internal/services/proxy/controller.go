package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// maxBodyBytes membatasi body chat completions yang dibaca ke memori
// (body harus dibaca penuh karena perlu diubah).
const maxBodyBytes = 20 << 20

// Controller menangani request HTTP untuk proxy.
type Controller struct {
	service *Service
}

// NewController membuat controller proxy.
func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// Handle menangani semua request /v1/*. POST /v1/chat/completions disisipi
// skill lebih dulu; path lain diteruskan polos ke 9Router.
func (h *Controller) Handle(c *gin.Context) {
	// key 9Router milik user wajib untuk semua /v1/*: middleware tidak
	// memakai key-nya sendiri untuk request client
	key := userctx.RouterKeyFromHeader(c)
	if key == "" {
		c.Data(http.StatusUnauthorized, "application/json", clients.OpenAIErrorBody(
			"header "+userctx.RouterKeyHeader+" wajib diisi dengan key 9Router milikmu", "invalid_api_key"))
		return
	}
	// alamat 9Router juga milik user: setiap user punya domain 9Router sendiri
	base, err := clients.ParseRouterURL(userctx.RouterURLFromHeader(c))
	if err != nil {
		c.Data(http.StatusBadRequest, "application/json", clients.OpenAIErrorBody(
			"header "+userctx.RouterURLHeader+" wajib berisi alamat 9Router milikmu, mis. https://host/v1: "+err.Error(),
			"invalid_request_error"))
		return
	}
	c.Request = c.Request.WithContext(userctx.WithRouterURL(userctx.WithRouterKey(c.Request.Context(), key), base))

	if c.Request.Method != http.MethodPost || c.Param("path") != "/chat/completions" {
		h.service.Forward(c.Writer, c.Request)
		return
	}

	// chat wajib membawa user_id: memori, gambar, log, dan MCP dipisah per user
	// user dari token login bila ada, selain itu dari header X-User-Id
	user := userctx.Resolve(c)
	if !userctx.Valid(user) {
		c.Data(http.StatusBadRequest, "application/json", clients.OpenAIErrorBody(
			"header "+userctx.Header+" wajib diisi (huruf, angka, . _ @ : -, maks 128)", "invalid_request_error"))
		return
	}
	c.Request = c.Request.WithContext(userctx.With(c.Request.Context(), user))

	start := time.Now()

	prepared, ok := h.injectSkill(c)
	if !ok {
		return
	}

	// dengan tool MCP, middleware menjalankan putaran panggil-tool sendiri;
	// tanpa tool, request diteruskan (dan di-stream) apa adanya
	var result clients.Result
	if len(prepared.ToolNames) > 0 {
		result = h.service.RunWithTools(c.Writer, c.Request, prepared)
	} else {
		result = h.service.Forward(c.Writer, c.Request)
	}
	h.service.RecordUsage(prepared, result, time.Since(start))
	h.service.Learn(c.Request.Context(), prepared)
}

// injectSkill membaca body, menyisipkan skill, lalu menggantinya pada request.
// Mengembalikan false bila body tidak bisa dibaca (respons error sudah dikirim).
func (h *Controller) injectSkill(c *gin.Context) (*Prepared, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes))
	if err != nil {
		status, msg := http.StatusBadRequest, "Body request tidak bisa dibaca"
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			status, msg = http.StatusRequestEntityTooLarge, "Body request terlalu besar"
		}
		c.Data(status, "application/json", clients.OpenAIErrorBody(msg, "invalid_request_error"))
		c.Abort()
		return nil, false
	}

	// bila error, prepared tetap berisi body asli (dan model/stream yang terbaca)
	meta := RequestMeta{
		UserID:         userctx.From(c.Request.Context()),
		ConversationID: c.GetHeader("X-Conversation-Id"),
	}
	prepared, err := h.service.Prepare(c.Request.Context(), raw, meta)
	if errors.Is(err, ErrModelRequired) {
		c.Data(http.StatusBadRequest, "application/json", clients.OpenAIErrorBody(
			"field model wajib diisi dengan model 9Router milikmu", "invalid_request_error"))
		c.Abort()
		return nil, false
	}
	if err != nil {
		// skill hanya bonus: request tetap diteruskan apa adanya
		logger.Warn("skill tidak disisipkan, request diteruskan tanpa perubahan", zap.Error(err))
	}

	// ekstraksi memori setelah respons memakai model yang sama dengan chat ini
	c.Request = c.Request.WithContext(userctx.WithRouterModel(c.Request.Context(), prepared.Model))
	c.Request.Body = io.NopCloser(bytes.NewReader(prepared.Body))
	c.Request.ContentLength = int64(len(prepared.Body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(prepared.Body)))

	if len(prepared.ToolNames) > 0 {
		c.Header("X-MCP-Tools", strconv.Itoa(len(prepared.ToolNames)))
	}
	if prepared.Skill != "" {
		c.Header("X-Skill", headerSafe(prepared.Skill))
		logger.Info("skill disisipkan", zap.String("skill", prepared.Skill), zap.Float64("score", prepared.Score))
	}
	return prepared, true
}

// headerSafe membuang karakter yang tidak boleh ada di nilai header.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
}
