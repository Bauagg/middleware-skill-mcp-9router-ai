package aiaccount

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// openaiDriver memakai layanan apa pun yang berformat API OpenAI
// (POST /images/generations dengan API key): admin cukup mengisi alamat dan
// model di provider, user menempel API key-nya. Tanpa koding.
type openaiDriver struct {
	images *clients.ImageClient
	http   *http.Client
}

func (openaiDriver) Info() DriverInfo {
	return DriverInfo{
		Key:         "openai",
		Name:        "API kompatibel OpenAI",
		Description: "Layanan apa pun berformat API OpenAI (mis. OpenAI, xAI Grok, OpenRouter, Groq): isi alamat API serta model chat dan/atau gambar, lalu user menempel API key. Biaya mengikuti pemakaian API, bukan kuota langganan.",
		Auth:        AuthAPIKey,
		Text:        true,
		Image:       true,

		NeedsBaseURL: true,
		ModelFields:  true,
	}
}

func (d openaiDriver) Open(p *Provider, secret map[string]string) Session {
	return &openaiSession{d: d, base: strings.TrimRight(p.BaseURL, "/"), chatModel: p.ChatModel, imageModel: p.ImageModel,
		key: secret[secretAPIKey]}
}

// secretAPIKey adalah nama rahasia akun AuthAPIKey.
const secretAPIKey = "api_key"

type openaiSession struct {
	noVideo
	d                                openaiDriver
	base, chatModel, imageModel, key string
}

// Check memanggil GET /models: key yang ditolak berarti ErrAuth.
func (o *openaiSession) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.key)
	resp, err := o.d.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s tidak dapat dihubungi: %w", o.base, err)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return classify(ErrAuth, errors.New("API key ditolak"))
	case resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound:
		// sebagian layanan tidak punya /models: 404 tidak dianggap gagal
		return fmt.Errorf("API menjawab status %d", resp.StatusCode)
	}
	return nil
}

// Refresh: API key tidak kedaluwarsa sendiri, tidak perlu dijaga.
func (o *openaiSession) Refresh(context.Context) error { return nil }

func (o *openaiSession) Secret() (map[string]string, bool) {
	return map[string]string{secretAPIKey: o.key}, false
}

func (o *openaiSession) Image(ctx context.Context, prompt, size string) (*clients.GeneratedImage, error) {
	ctx = userctx.WithRouterKey(userctx.WithRouterURL(ctx, o.base), o.key)
	img, err := o.d.images.Generate(ctx, o.imageModel, prompt, size)
	var ie *clients.ImageError
	if errors.As(err, &ie) {
		switch ie.Status {
		case http.StatusUnauthorized:
			return nil, classify(ErrAuth, err)
		case http.StatusPaymentRequired, http.StatusTooManyRequests:
			return nil, classify(ErrUsageLimit, err)
		}
	}
	return img, err
}

// Chat memanggil POST /chat/completions (tanpa streaming) dengan model chat provider.
func (o *openaiSession) Chat(ctx context.Context, messages []clients.ChatMessage) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": o.chatModel, "messages": messages, "stream": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.key)
	resp, err := o.d.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s tidak dapat dihubungi: %w", o.base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 {
		err := fmt.Errorf("API menjawab %d: %s", resp.StatusCode, clip(out.Error.Message, 300))
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return "", classify(ErrAuth, err)
		case http.StatusPaymentRequired, http.StatusTooManyRequests:
			return "", classify(ErrUsageLimit, err)
		}
		return "", err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("API tidak mengembalikan jawaban")
	}
	return out.Choices[0].Message.Content, nil
}
