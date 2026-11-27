// Package clients berisi klien untuk layanan pihak ketiga yang dipanggil
// middleware. Satu file per layanan, supaya urusan koneksi, timeout, dan
// format error layanan itu terkumpul di satu tempat dan mudah dirawat.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/openai"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// Result adalah hasil pengamatan satu penerusan request ke 9Router.
type Result struct {
	// Status adalah status dari 9Router (atau dari proxy bila 9Router tidak
	// terjangkau). 0 bila tidak ada respons sama sekali.
	Status int

	// Usage adalah pemakaian token yang dilaporkan 9Router; nil bila tidak
	// dilaporkan (mis. streaming tanpa stream_options.include_usage).
	Usage *openai.Usage

	// Aborted true bila client memutus koneksi sebelum respons selesai.
	Aborted bool

	// Fallback true bila 9Router gagal dan responsnya TIDAK diteruskan ke
	// client (lihat WithFallback): pemanggil wajib menjawab sendiri.
	// UpstreamError berisi pesan error 9Router-nya.
	Fallback      bool
	UpstreamError string
}

// fallbackKey menandai request yang boleh dijawab pihak lain bila 9Router gagal.
type fallbackKey struct{}

// WithFallback membuat Forward menahan respons gagal dari 9Router (dan
// kegagalan menghubunginya) bila accept(status) true: tidak ada yang ditulis
// ke client, dan Result.Fallback diisi supaya pemanggil menjawab sendiri.
func WithFallback(ctx context.Context, accept func(status int) bool) context.Context {
	return context.WithValue(ctx, fallbackKey{}, accept)
}

func fallbackFrom(ctx context.Context) func(int) bool {
	f, _ := ctx.Value(fallbackKey{}).(func(int) bool)
	return f
}

// errHeldBack: respons gagal ditahan untuk dijawab pihak lain (WithFallback).
var errHeldBack = errors.New("respons 9Router ditahan untuk cadangan")

// NineRouter adalah klien ke 9Router: meneruskan request apa adanya,
// mengalirkan responsnya kembali ke client, dan mengamati status serta
// pemakaian token tanpa menunda aliran.
type NineRouter struct {
	proxy *httputil.ReverseProxy

	// untuk panggilan middleware sendiri (Complete), bukan request client
	http *http.Client
}

// observer menampung hasil pengamatan satu request. Hanya disentuh dari
// goroutine yang menjalankan ReverseProxy.ServeHTTP, jadi tanpa lock.
type observer struct {
	status   int
	aborted  bool
	sniffer  *openai.UsageSniffer
	fallback bool
	upstream string // pesan error 9Router yang ditahan
}

type observerKey struct{}

func observerFrom(ctx context.Context) *observer {
	obs, _ := ctx.Value(observerKey{}).(*observer)
	return obs
}

// teeBody menyalin byte yang dibaca ke sink tanpa mengubahnya.
type teeBody struct {
	io.ReadCloser
	sink io.Writer
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 {
		_, _ = t.sink.Write(p[:n])
	}
	return n, err
}

// NewNineRouter membuat klien 9Router. Middleware tidak punya 9Router sendiri:
// alamat (header X-Router-URL) dan key (header X-Router-Key) selalu milik
// user, diambil dari context. Path request "/v1/xyz" dipetakan ke
// <alamat user> + "/xyz". allowPrivate mengizinkan alamat di jaringan
// internal (hanya untuk pengembangan). Header
// Authorization dari client selalu diganti, dan header milik middleware tidak
// ikut diteruskan.
func NewNineRouter(allowPrivate bool) *NineRouter {
	transport := newRouterTransport(allowPrivate)
	// model bisa lama sebelum mengirim byte pertama (mis. model reasoning)
	transport.ResponseHeaderTimeout = 5 * time.Minute

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			// alamat sudah divalidasi controller; bila tidak ada, Host kosong
			// membuat request gagal dan dijawab 502 oleh ErrorHandler
			target, _ := url.Parse(userctx.RouterURL(pr.In.Context()))
			if target == nil {
				target = &url.URL{}
			}
			out.URL.Scheme = target.Scheme
			out.URL.Host = target.Host
			out.URL.Path = target.Path + strings.TrimPrefix(pr.In.URL.Path, "/v1")
			out.URL.RawPath = ""
			// 9Router (di belakang virtual host/CDN) perlu Host miliknya, bukan Host middleware
			out.Host = target.Host

			// tanpa ini respons bisa datang terkompresi dan token tidak bisa dibaca;
			// bila dihapus, Go meminta gzip sendiri lalu mendekompresinya otomatis
			out.Header.Del("Accept-Encoding")

			// key 9Router milik user (X-Router-Key); header milik middleware
			// (secret aplikasi, user_id) tidak perlu diketahui 9Router
			out.Header.Del("Authorization")
			if key := userctx.RouterKey(pr.In.Context()); key != "" {
				out.Header.Set("Authorization", "Bearer "+key)
			}
			for _, h := range []string{"X-Api-Key", "X-Router-Key", "X-Router-Url", "X-User-Id"} {
				out.Header.Del(h)
			}
		},
		Transport: transport,

		// -1 = flush setiap potongan; wajib agar streaming SSE tidak tertahan
		FlushInterval: -1,

		ModifyResponse: func(resp *http.Response) error {
			if accept := fallbackFrom(resp.Request.Context()); accept != nil && resp.StatusCode/100 != 2 && accept(resp.StatusCode) {
				if obs := observerFrom(resp.Request.Context()); obs != nil {
					raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
					obs.status, obs.fallback, obs.upstream = resp.StatusCode, true, upstreamMessage(raw)
				}
				return errHeldBack // ErrorHandler dipanggil, body ditutup proxy
			}
			if obs := observerFrom(resp.Request.Context()); obs != nil {
				obs.status = resp.StatusCode
				obs.sniffer = openai.NewUsageSniffer(resp.Header.Get("Content-Type"))
				resp.Body = &teeBody{ReadCloser: resp.Body, sink: obs.sniffer}
			}
			return nil
		},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			obs := observerFrom(r.Context())

			if errors.Is(err, context.Canceled) {
				if obs != nil {
					obs.aborted = true
				}
				return // client memutus koneksi, tidak ada yang perlu dibalas
			}

			if errors.Is(err, errHeldBack) {
				return // pemanggil yang menjawab (WithFallback)
			}

			status, typ, msg := http.StatusBadGateway, "bad_gateway", "9Router tidak dapat dihubungi"
			var ne net.Error
			if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
				status, typ, msg = http.StatusGatewayTimeout, "gateway_timeout", "9Router tidak merespons tepat waktu"
			}
			if obs != nil {
				obs.status = status
			}
			if accept := fallbackFrom(r.Context()); accept != nil && obs != nil && accept(status) {
				logger.Warn("9Router gagal, dialihkan ke cadangan", zap.Error(err), zap.String("path", r.URL.Path))
				obs.fallback, obs.upstream = true, msg
				return
			}

			logger.Error("meneruskan ke 9Router gagal", zap.Error(err), zap.String("path", r.URL.Path))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(OpenAIErrorBody(msg, typ))
		},
	}
	return &NineRouter{
		proxy: rp,
		http:  &http.Client{Transport: transport},
	}
}

// Forward meneruskan request ke 9Router, mengalirkan responsnya ke w, lalu
// mengembalikan hasil pengamatannya setelah respons selesai.
func (n *NineRouter) Forward(w http.ResponseWriter, req *http.Request) (res Result) {
	obs := &observer{}
	req = req.WithContext(context.WithValue(req.Context(), observerKey{}, obs))

	// client yang memutus koneksi di tengah streaming membuat ReverseProxy
	// panic dengan http.ErrAbortHandler; itu perilaku normal, bukan error
	defer func() {
		if r := recover(); r != nil {
			if r != http.ErrAbortHandler {
				panic(r)
			}
			obs.aborted = true
			res = obs.result()
		}
	}()

	n.proxy.ServeHTTP(w, req)
	return obs.result()
}

func (o *observer) result() Result {
	res := Result{Status: o.status, Aborted: o.aborted, Fallback: o.fallback, UpstreamError: o.upstream}
	if o.sniffer != nil {
		res.Usage = o.sniffer.Usage()
	}
	return res
}

// upstreamMessage mengambil pesan error dari body error 9Router.
func upstreamMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return innerMessage(e.Error.Message)
	}
	return snippet(raw)
}

// OpenAIErrorBody membuat body error ala OpenAI ({"error":{"message","type"}}).
// Dipakai untuk error yang berasal dari proxy sendiri, karena client di jalur
// ini adalah SDK OpenAI yang membaca field "error", bukan amplop API kita.
func OpenAIErrorBody(message, typ string) []byte {
	type detail struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	b, _ := json.Marshal(struct {
		Error detail `json:"error"`
	}{Error: detail{Message: message, Type: typ}})
	return b
}

// ChatMessage adalah satu pesan untuk Complete.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Completion adalah hasil Complete.
type Completion struct {
	Text  string
	Usage *openai.Usage // nil bila tidak dilaporkan
}

// maxCompletionBytes membatasi respons yang dibaca Complete.
const maxCompletionBytes = 8 << 20

// Complete memanggil POST /chat/completions (tanpa streaming) memakai API key
// middleware. Dipakai untuk pekerjaan middleware sendiri (pemilih cadangan,
// eval), bukan untuk request client. Batas waktu diatur lewat ctx.
//
// Parameter seperti temperature dan max_tokens sengaja tidak dikirim, karena
// sebagian model (mis. model reasoning) menolaknya; panjang jawaban diatur
// lewat instruksi di prompt.
func (n *NineRouter) Complete(ctx context.Context, model string, messages []ChatMessage) (*Completion, error) {
	body, err := json.Marshal(map[string]any{"model": model, "messages": messages, "stream": false})
	if err != nil {
		return nil, err
	}

	base, err := routerBase(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	key, err := routerKey(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := n.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("9Router tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCompletionBytes))
	if err != nil {
		return nil, fmt.Errorf("baca respons 9Router: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("9Router menjawab %d: %s", resp.StatusCode, snippet(raw))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return nil, fmt.Errorf("respons 9Router tidak dikenali: %s", snippet(raw))
	}

	sniffer := openai.NewUsageSniffer("application/json")
	_, _ = sniffer.Write(raw)

	return &Completion{Text: strings.TrimSpace(messageText(out.Choices[0].Message.Content)), Usage: sniffer.Usage()}, nil
}

// ChatStream mengirim body chat completions (stream: true) memakai key 9Router
// milik user (dari context) dan mengembalikan respons apa adanya; pemanggil
// membaca SSE-nya dan wajib menutup Body. Dipakai putaran tool: streaming
// membuat koneksi tidak pernah diam, sehingga jawaban panjang tidak diputus
// Cloudflare (sekitar 100 detik tanpa data).
func (n *NineRouter) ChatStream(ctx context.Context, body []byte) (*http.Response, error) {
	base, err := routerBase(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	key, err := routerKey(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return n.http.Do(req)
}

// ErrNoRouterKey berarti panggilan ke 9Router tidak membawa key milik user.
var ErrNoRouterKey = errors.New("key 9Router milik user tidak ada (header X-Router-Key)")

// routerKey mengambil key 9Router milik user dari context.
func routerKey(ctx context.Context) (string, error) {
	if k := userctx.RouterKey(ctx); k != "" {
		return k, nil
	}
	return "", ErrNoRouterKey
}

// messageText membaca content jawaban yang bisa berupa string atau array bagian.
func messageText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if p.Text != "" && (p.Type == "text" || p.Type == "") {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// snippet memotong body error supaya pesan error tetap pendek.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
