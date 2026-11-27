// Package chatgptweb adalah klien ChatGPT Web (chatgpt.com) memakai sesi
// login (cookie), untuk memakai kuota langganan ChatGPT user tanpa API key.
// Protokolnya tidak resmi: bila OpenAI mengubah web-nya, package ini perlu
// disesuaikan.
package chatgptweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

const (
	urlSession = "https://chatgpt.com/api/auth/session"

	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
)

var (
	// ErrAuth: sesi tidak berlaku (cookie kedaluwarsa atau logout).
	ErrAuth = errors.New("sesi ChatGPT tidak berlaku: login ulang")
	// ErrBlocked: Cloudflare/OpenAI menolak request dari server ini.
	ErrBlocked = errors.New("ChatGPT menolak request dari server ini (proteksi bot)")
)

// Session adalah satu akun ChatGPT yang sudah login. Aman dipakai bersamaan.
type Session struct {
	http *http.Client

	mu      sync.Mutex
	cookies map[string]string
	changed bool
	token   string // access token dari /api/auth/session
	email   string
}

// NewSession membuat sesi dari cookie akun.
func NewSession(cookies map[string]string) *Session {
	m := make(map[string]string, len(cookies))
	for k, v := range cookies {
		m[k] = v
	}
	return &Session{http: &http.Client{}, cookies: m} // batas waktu lewat ctx
}

// Cookies mengembalikan cookie terbaru dan apakah berubah sejak pemanggilan
// sebelumnya, supaya disimpan ulang.
func (s *Session) Cookies() (map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]string, len(s.cookies))
	for k, v := range s.cookies {
		m[k] = v
	}
	changed := s.changed
	s.changed = false
	return m, changed
}

// Email akun, terisi setelah Check berhasil.
func (s *Session) Email() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.email
}

// Check membuka /api/auth/session: bila sesi berlaku, ChatGPT memberi access
// token baru dan memperbarui cookie session-token (jadi sekaligus menjaga
// sesi tetap hidup).
func (s *Session) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlSession, nil)
	if err != nil {
		return err
	}
	s.decorate(req)
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("chatgpt.com tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	s.absorb(resp)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return ErrBlocked
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrAuth
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("ChatGPT menjawab status %d", resp.StatusCode)
	}
	var v struct {
		AccessToken string `json:"accessToken"`
		User        struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return ErrBlocked // halaman HTML tantangan Cloudflare, bukan JSON
	}
	if v.AccessToken == "" {
		return ErrAuth
	}
	s.mu.Lock()
	s.token, s.email = v.AccessToken, v.User.Email
	s.mu.Unlock()
	return nil
}

func (s *Session) decorate(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("Cookie", s.cookieHeader())
}

func (s *Session) cookieHeader() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.cookies))
	for k := range s.cookies {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = k + "=" + s.cookies[k]
	}
	return strings.Join(parts, "; ")
}

// absorb menyerap Set-Cookie dari respons chatgpt.com.
func (s *Session) absorb(resp *http.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			continue // penghapusan cookie tidak diikuti: sesi lebih penting
		}
		if s.cookies[c.Name] != c.Value {
			s.cookies[c.Name] = c.Value
			s.changed = true
		}
	}
}
