// Package geminiweb adalah klien Gemini Web (gemini.google.com) memakai sesi
// login Google (cookie), untuk membuat gambar dan video dengan kuota akun
// Gemini user, tanpa API key. Protokolnya tidak resmi: bila Google mengubah
// web-nya, bagian yang membaca respons di package ini perlu disesuaikan.
package geminiweb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	urlInit     = "https://gemini.google.com/app"
	urlGenerate = "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"
	urlBatch    = "https://gemini.google.com/_/BardChatUi/data/batchexecute"
	urlRotate   = "https://accounts.google.com/RotateCookies"

	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
)

var (
	reToken    = regexp.MustCompile(`"SNlM0e":\s*"(.*?)"`)
	reBuild    = regexp.MustCompile(`"cfb2h":\s*"(.*?)"`)
	reSession  = regexp.MustCompile(`"FdrFJe":\s*"(.*?)"`)
	reLanguage = regexp.MustCompile(`"TuX5cc":\s*"(.*?)"`)
	reEmail    = regexp.MustCompile(`"oPEP7c":\s*"([^"@\s]+@[^"\s]+)"`)
)

// Kesalahan yang dibedakan pemanggil untuk memilih tindakan (login ulang,
// jeda akun, coba akun lain).
var (
	// ErrAuth: sesi tidak berlaku (cookie kedaluwarsa atau logout); perlu cookie baru.
	ErrAuth = errors.New("sesi Google tidak berlaku: tempel cookie baru")
	// ErrUsageLimit: kuota akun untuk fitur ini habis.
	ErrUsageLimit = errors.New("limit akun Gemini habis")
	// ErrBlocked: Google membatasi IP ini sementara (traffic tidak wajar).
	ErrBlocked = errors.New("Google membatasi IP ini sementara (traffic tidak wajar)")
)

// Session adalah satu akun Google yang sudah login. Aman dipakai bersamaan.
type Session struct {
	http *http.Client
	jar  *jar

	mu       sync.Mutex
	token    string // SNlM0e
	build    string // cfb2h, dikirim sebagai bl
	sid      string // FdrFJe, dikirim sebagai f.sid
	lang     string
	email    string // email akun Google, bila tercantum di halaman
	initAt   time.Time
	reqID    int
	rotateAt time.Time
}

// NewSession membuat sesi dari cookie akun. Belum ada request: panggil Init.
func NewSession(cookies map[string]string) *Session {
	s := &Session{jar: newJar(cookies), lang: "en", reqID: 10000 + rand.IntN(90000)}
	s.http = &http.Client{CheckRedirect: s.followRedirect} // batas waktu lewat ctx
	return s
}

// followRedirect membawa cookie ke lompatan berikutnya (hanya domain Google)
// dan menyerap cookie baru dari respons redirect.
func (s *Session) followRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("terlalu banyak redirect")
	}
	s.jar.absorb(req.Response)
	if isGoogleHost(req.URL.Hostname()) {
		req.Header.Set("Cookie", s.jar.header(nil))
	} else {
		req.Header.Del("Cookie")
	}
	return nil
}

func isGoogleHost(h string) bool { return h == "google.com" || strings.HasSuffix(h, ".google.com") }

// Cookies mengembalikan cookie terbaru dan apakah berubah sejak pemanggilan
// sebelumnya (Google memperbaruinya berkala), supaya disimpan ulang.
func (s *Session) Cookies() (map[string]string, bool) { return s.jar.snapshot() }

// Email mengembalikan email akun Google (terisi setelah Init, bila tercantum).
func (s *Session) Email() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.email
}

// Ready melaporkan apakah sesi sudah punya token.
func (s *Session) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token != ""
}

// tokenMaxAge: token SNlM0e punya masa berlaku; diambil ulang sebelum habis.
const tokenMaxAge = 30 * time.Minute

// Refresh menjaga sesi tetap hidup: mengambil token baru bila belum ada atau
// sudah lama, lalu memperbarui cookie. Dipanggil berkala walau akun jarang
// dipakai, supaya login cukup sekali.
func (s *Session) Refresh(ctx context.Context) error {
	s.mu.Lock()
	stale := s.token == "" || time.Since(s.initAt) > tokenMaxAge
	s.mu.Unlock()
	if stale {
		if err := s.Init(ctx); err != nil {
			return err
		}
	}
	return s.Rotate(ctx)
}

// Init membuka gemini.google.com/app dan mengambil token sesi. Sesi yang
// dialihkan ke halaman login berarti cookie tidak berlaku (ErrAuth).
func (s *Session) Init(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlInit, nil)
	if err != nil {
		return err
	}
	s.decorate(req, nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("gemini.google.com tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	s.jar.absorb(resp)
	if err := statusError(resp); err != nil {
		return err
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}

	token := match(reToken, page)
	if token == "" || resp.Request.URL.Hostname() != "gemini.google.com" {
		return ErrAuth
	}
	s.mu.Lock()
	s.token, s.build, s.sid = token, match(reBuild, page), match(reSession, page)
	if l := match(reLanguage, page); l != "" {
		s.lang = l
	}
	if e := match(reEmail, page); e != "" {
		s.email = e
	}
	s.initAt = time.Now()
	s.mu.Unlock()
	return nil
}

func match(re *regexp.Regexp, b []byte) string {
	if m := re.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// Rotate memperbarui cookie __Secure-1PSIDTS supaya sesi tidak kedaluwarsa.
// Google menolak bila terlalu sering, jadi paling cepat sekali per menit.
func (s *Session) Rotate(ctx context.Context) error {
	s.mu.Lock()
	if time.Since(s.rotateAt) < time.Minute {
		s.mu.Unlock()
		return nil
	}
	s.rotateAt = time.Now()
	s.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlRotate, strings.NewReader(`[000,"-0000000000000000000"]`))
	if err != nil {
		return err
	}
	s.decorate(req, map[string]string{"Content-Type": "application/json", "Origin": "https://accounts.google.com"})
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("perbarui cookie gagal: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	s.jar.absorb(resp)
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrAuth
	}
	return statusError(resp)
}

// decorate memasang header umum: cookie, user agent, dan header tambahan.
func (s *Session) decorate(req *http.Request, extra map[string]string) {
	req.Header.Set("Cookie", s.jar.header(nil))
	req.Header.Set("User-Agent", userAgent)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
}

// rpcParams menyusun query umum request ke backend Gemini.
func (s *Session) rpcParams() (url.Values, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqID += 100000
	q := url.Values{"hl": {s.lang}, "_reqid": {fmt.Sprint(s.reqID)}, "rt": {"c"}}
	if s.build != "" {
		q.Set("bl", s.build)
	}
	if s.sid != "" {
		q.Set("f.sid", s.sid)
	}
	return q, s.token
}

// statusError menerjemahkan status HTTP Google menjadi kesalahan yang dikenali.
func statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrBlocked
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrAuth
	default:
		return fmt.Errorf("Gemini menjawab status %d", resp.StatusCode)
	}
}
