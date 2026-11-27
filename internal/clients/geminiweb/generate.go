package geminiweb

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Kode kesalahan Gemini di dalam respons (bukan status HTTP).
const (
	codeTemporary     = 1013
	codeUsageLimit    = 1037
	codeModelMismatch = 1050
	codeModelInvalid  = 1052
	codeIPBlocked     = 1060
)

// maxReplyBytes membatasi ukuran satu respons generate.
const maxReplyBytes = 32 << 20

// Reply adalah jawaban Gemini untuk satu prompt.
type Reply struct {
	Text   string
	Images []GeneratedImage
	Videos []GeneratedVideo

	// pengenal percakapan, dibutuhkan untuk mengambil gambar ukuran penuh
	cid, rid, rcid string
}

// GeneratedImage adalah satu gambar buatan Gemini (belum diunduh).
type GeneratedImage struct {
	URL string // preview; ukuran penuh diambil lewat Session.DownloadImage
	ID  string
	Alt string
}

// GeneratedVideo adalah satu video buatan Gemini (Veo), belum diunduh.
type GeneratedVideo struct {
	URL       string
	Thumbnail string
}

// placeholder link yang Gemini sisipkan di teks sebagai tempat lampiran
var reArtifact = regexp.MustCompile(`https?://googleusercontent\.com/\S+\s*`)

// Generate mengirim satu prompt sebagai percakapan baru dan menunggu jawabannya
// selesai. Video bisa membuat respons diam beberapa menit, jadi batas waktunya
// diatur pemanggil lewat ctx.
func (s *Session) Generate(ctx context.Context, prompt string) (*Reply, error) {
	if !s.Ready() {
		if err := s.Init(ctx); err != nil {
			return nil, err
		}
	}
	r, err := s.generate(ctx, prompt)
	if !errors.Is(err, ErrAuth) {
		return r, err
	}
	// token bisa kedaluwarsa walau cookie masih berlaku: ambil token baru dan
	// coba sekali lagi sebelum menyimpulkan sesinya mati
	if err := s.Init(ctx); err != nil {
		return nil, err
	}
	return s.generate(ctx, prompt)
}

func (s *Session) generate(ctx context.Context, prompt string) (*Reply, error) {
	q, token := s.rpcParams()
	s.mu.Lock()
	lang := s.lang
	s.mu.Unlock()

	id := newUUID()
	req := make([]any, 81)
	req[0] = []any{prompt, 0, nil, nil, nil, nil, 0}
	req[1] = []any{lang}
	req[2] = []any{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	req[6] = []any{1}
	req[7] = 1 // streaming
	req[10] = 1
	req[11] = 0
	req[17] = []any{[]any{0}}
	req[18] = 0
	req[27] = 1
	req[30] = []any{4}
	req[41] = []any{1}
	req[53] = 0
	req[59] = id
	req[61] = []any{}
	req[68] = 1
	req[79] = 1
	req[80] = 1
	inner, _ := json.Marshal(req)
	freq, _ := json.Marshal([]any{nil, string(inner)})
	form := url.Values{"at": {token}, "f.req": {string(freq)}}

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, urlGenerate+"?"+q.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	s.decorate(hr, map[string]string{
		"Content-Type":              "application/x-www-form-urlencoded;charset=utf-8",
		"Origin":                    "https://gemini.google.com",
		"Referer":                   "https://gemini.google.com/",
		"X-Same-Domain":             "1",
		"x-goog-ext-525005358-jspb": `["` + id + `",1]`,
	})
	resp, err := s.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("Gemini tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	s.jar.absorb(resp)
	if err := statusError(resp); err != nil {
		if err == ErrAuth {
			s.resetToken()
		}
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return nil, fmt.Errorf("baca jawaban Gemini: %w", err)
	}
	return parseReply(string(body))
}

func (s *Session) resetToken() {
	s.mu.Lock()
	s.token = ""
	s.mu.Unlock()
}

// parseReply membaca semua envelope jawaban. Setiap envelope berisi keadaan
// jawaban sejauh itu, jadi yang dipakai adalah nilai terakhir yang terisi.
func parseReply(body string) (*Reply, error) {
	r := &Reply{}
	for _, env := range frames(body) {
		if code, ok := at(env, 5, 2, 0, 1, 0).(float64); ok && code != 0 {
			return nil, replyError(int(code))
		}
		part := innerJSON(str(env, 2))
		if part == nil {
			continue
		}
		if v := str(part, 1, 0); v != "" {
			r.cid = v
		}
		if v := str(part, 1, 1); v != "" {
			r.rid = v
		}
		cands := list(part, 4)
		if len(cands) == 0 {
			continue
		}
		c := cands[0]
		if v := str(c, 0); v != "" {
			r.rcid = v
		}
		if t := str(c, 1, 0); t != "" {
			r.Text = t
		}
		rich := at(c, 12)
		if imgs := list(field(rich, 7), 0); len(imgs) > 0 {
			r.Images = r.Images[:0]
			for _, g := range imgs {
				if u := str(g, 0, 3, 3); u != "" {
					r.Images = append(r.Images, GeneratedImage{URL: u, ID: str(g, 1, 0), Alt: str(g, 0, 3, 2)})
				}
			}
		}
		if urls := list(field(rich, 59), 0, 0, 0, 0, 7); len(urls) >= 2 {
			u, _ := urls[1].(string)
			th, _ := urls[0].(string)
			if u != "" {
				r.Videos = []GeneratedVideo{{URL: u, Thumbnail: th}}
			}
		}
	}
	r.Text = strings.TrimSpace(reArtifact.ReplaceAllString(r.Text, ""))
	return r, nil
}

func replyError(code int) error {
	switch code {
	case codeUsageLimit:
		return ErrUsageLimit
	case codeIPBlocked:
		return ErrBlocked
	case codeTemporary:
		return fmt.Errorf("Gemini sedang error sementara (%d), coba lagi", code)
	case codeModelMismatch, codeModelInvalid:
		return fmt.Errorf("format request ditolak Gemini (%d): protokol mungkin berubah", code)
	default:
		return fmt.Errorf("Gemini menjawab kode error %d", code)
	}
}

// newUUID membuat UUID v4 huruf besar seperti yang dikirim web Gemini.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]))
}
