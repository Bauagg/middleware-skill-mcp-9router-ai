package aiaccount

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	gw "github.com/Bauagg/middleware-skill-ai/internal/clients/geminiweb"
)

// geminiDriver memakai Gemini Web (gemini.google.com) dengan sesi login Google.
type geminiDriver struct{}

func (geminiDriver) Info() DriverInfo {
	return DriverInfo{
		Key:         "gemini",
		Name:        "Gemini",
		Description: "Akun Google dengan Gemini (mis. Google AI Plus/Pro): chat (cadangan saat 9Router habis), gambar, dan video Veo memakai kuota langganan akun.",
		Auth:        AuthBrowser,
		Text:        true,
		Image:       true,
		Video:       true,

		LoginURL:       "https://accounts.google.com/ServiceLogin?continue=https%3A%2F%2Fgemini.google.com%2Fapp",
		CookieDomains:  []string{"google.com"},
		SessionCookies: []string{"__Secure-1PSID"},
		Builtin:        true,
	}
}

func (geminiDriver) Open(_ *Provider, secret map[string]string) Session {
	return &geminiSession{s: gw.NewSession(secret)}
}

type geminiSession struct{ s *gw.Session }

// Jawaban Gemini tanpa gambar/video yang berarti limitnya habis.
var reLimit = regexp.MustCompile(`(?i)limit|quota|try again (later|in|after)|reached|batas|kuota|coba lagi (nanti|dalam)`)

// Jawaban Gemini yang berarti error sementara (bukan penolakan).
var reGlitch = regexp.MustCompile(`(?i)encountered an error|something went wrong|terjadi (kesalahan|error)`)

func (g *geminiSession) Check(ctx context.Context) error {
	err := g.s.Init(ctx)
	if err == nil {
		err = g.s.Rotate(ctx)
	}
	return geminiErr(err)
}

func (g *geminiSession) Email() string { return g.s.Email() }

func (g *geminiSession) Refresh(ctx context.Context) error { return geminiErr(g.s.Refresh(ctx)) }

func (g *geminiSession) Secret() (map[string]string, bool) { return g.s.Cookies() }

// Chat mengirim seluruh percakapan sebagai satu pesan: Gemini Web tidak
// mengenal pesan system dan riwayat dari luar, jadi keduanya ditulis sebagai teks.
func (g *geminiSession) Chat(ctx context.Context, messages []clients.ChatMessage) (string, error) {
	r, err := g.s.Generate(ctx, transcript(messages, maxPromptRunes))
	if err != nil {
		return "", geminiErr(err)
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return "", errors.New("Gemini tidak menjawab")
	}
	return text, nil
}

func (g *geminiSession) Image(ctx context.Context, prompt, _ string) (*clients.GeneratedImage, error) {
	r, err := g.s.Generate(ctx, "Generate an image: "+prompt)
	if err != nil {
		return nil, geminiErr(err)
	}
	if len(r.Images) == 0 {
		if reLimit.MatchString(r.Text) {
			return nil, classify(ErrUsageLimit, errors.New("limit gambar Gemini habis"))
		}
		return nil, fmt.Errorf("Gemini tidak membuat gambar: %s", clip(r.Text, 300))
	}
	data, err := g.s.DownloadImage(ctx, r, r.Images[0])
	if err != nil {
		return nil, geminiErr(err)
	}
	return &clients.GeneratedImage{Data: data, MIME: http.DetectContentType(data)}, nil
}

func (g *geminiSession) Video(ctx context.Context, prompt string) ([]byte, error) {
	r, err := g.s.Generate(ctx, "Create a video (not an image): "+prompt)
	if err != nil {
		return nil, geminiErr(err)
	}
	switch {
	case len(r.Videos) > 0:
	case len(r.Images) > 0, reLimit.MatchString(r.Text):
		// gambar sebagai ganti video = limit video harian habis
		return nil, classify(ErrUsageLimit, errors.New("limit video Gemini habis"))
	case reGlitch.MatchString(r.Text):
		return nil, fmt.Errorf("Gemini sedang error sementara: %s", clip(r.Text, 200))
	default:
		return nil, fmt.Errorf("Gemini tidak membuat video: %s", clip(r.Text, 300))
	}
	data, err := g.s.DownloadVideo(ctx, r.Videos[0])
	return data, geminiErr(err)
}

// geminiErr menerjemahkan kesalahan klien Gemini ke kesalahan Service.
func geminiErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gw.ErrAuth):
		return classify(ErrAuth, errors.New("sesi Google tidak berlaku: login ulang"))
	case errors.Is(err, gw.ErrUsageLimit):
		return classify(ErrUsageLimit, err)
	case errors.Is(err, gw.ErrBlocked):
		return classify(ErrBlocked, err)
	}
	return err
}
