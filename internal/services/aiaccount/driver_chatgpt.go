package aiaccount

import (
	"context"
	"errors"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/clients/chatgptweb"
)

// chatgptDriver memakai ChatGPT Web (chatgpt.com) dengan sesi login.
type chatgptDriver struct{}

func (chatgptDriver) Info() DriverInfo {
	return DriverInfo{
		Key:         "chatgpt",
		Name:        "ChatGPT",
		Description: "Akun ChatGPT (mis. Plus/Pro): memakai kuota langganan akun.",
		Auth:        AuthBrowser,
		Note:        "Pembuatan gambar lewat ChatGPT sedang disiapkan: saat ini akun bisa login dan sesinya dijaga tetap hidup.",

		LoginURL:       "https://chatgpt.com/auth/login",
		CookieDomains:  []string{"chatgpt.com"},
		SessionCookies: []string{"__Secure-next-auth.session-token*"},
		Builtin:        true,
	}
}

func (chatgptDriver) Open(_ *Provider, secret map[string]string) Session {
	return &chatgptSession{s: chatgptweb.NewSession(secret)}
}

type chatgptSession struct {
	noVideo
	s *chatgptweb.Session
}

func (c *chatgptSession) Check(ctx context.Context) error { return chatgptErr(c.s.Check(ctx)) }

// Refresh: /api/auth/session sekaligus memperbarui cookie session-token.
func (c *chatgptSession) Refresh(ctx context.Context) error { return chatgptErr(c.s.Check(ctx)) }

func (c *chatgptSession) Email() string { return c.s.Email() }

func (c *chatgptSession) Secret() (map[string]string, bool) { return c.s.Cookies() }

func (c *chatgptSession) Chat(context.Context, []clients.ChatMessage) (string, error) {
	return "", ErrUnsupported
}

func (c *chatgptSession) Image(context.Context, string, string) (*clients.GeneratedImage, error) {
	return nil, ErrUnsupported
}

func chatgptErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, chatgptweb.ErrAuth):
		return classify(ErrAuth, err)
	case errors.Is(err, chatgptweb.ErrBlocked):
		return classify(ErrBlocked, err)
	}
	return err
}
