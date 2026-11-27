package clients

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxImageBytes membatasi ukuran satu gambar yang diterima.
const maxImageBytes = 25 << 20

// ImageError adalah penolakan dari layanan gambar beserta status HTTP-nya,
// supaya pemanggil bisa membedakan "akun tidak berhak" (403) dari "sedang
// penuh" (503).
type ImageError struct {
	Status  int
	Message string
}

func (e *ImageError) Error() string {
	return fmt.Sprintf("layanan gambar menjawab %d: %s", e.Status, e.Message)
}

// GeneratedImage adalah satu gambar hasil generate.
type GeneratedImage struct {
	Data          []byte
	MIME          string // image/png, image/jpeg, image/webp
	RevisedPrompt string // prompt yang dipakai provider (bila dilaporkan)
	Model         string // model yang sebenarnya membuat gambar, bila berbeda dari yang diminta
}

// ImageClient memanggil endpoint pembuat gambar berformat OpenAI
// (POST /images/generations), mis. lewat 9Router.
type ImageClient struct {
	http *http.Client
}

// NewImageClient membuat klien. Alamat dan key 9Router selalu milik user
// yang sedang chat (dari context). Transport-nya sama-sama menolak alamat
// internal, termasuk saat mengunduh gambar dari URL balasan provider.
func NewImageClient(allowPrivate bool) *ImageClient {
	return &ImageClient{
		http: &http.Client{Transport: newRouterTransport(allowPrivate)}, // batas waktu lewat ctx
	}
}

// ImageModels mengambil daftar model gambar yang tersedia di akun 9Router
// user (GET /models/image). Model gambar tidak ditulis mati di .env: setiap
// user bisa punya provider gambar berbeda di 9Router-nya.
func (c *ImageClient) ImageModels(ctx context.Context) ([]string, error) {
	key, err := routerKey(ctx)
	if err != nil {
		return nil, err
	}
	base, err := routerBase(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models/image", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daftar model gambar tidak dapat diambil: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &ImageError{Status: resp.StatusCode, Message: innerMessage(string(raw))}
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("daftar model gambar tidak dikenali: %w", err)
	}
	models := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

// Generate membuat satu gambar dari prompt. Provider bisa mengembalikan
// gambar sebagai base64 atau URL; URL langsung diunduh karena biasanya hanya
// berlaku sebentar.
func (c *ImageClient) Generate(ctx context.Context, model, prompt, size string) (*GeneratedImage, error) {
	req := map[string]any{"model": model, "prompt": prompt, "n": 1}
	if size != "" {
		req["size"] = size
	}
	body, _ := json.Marshal(req)

	base, err := routerBase(ctx)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	key, err := routerKey(ctx)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Authorization", "Bearer "+key)

	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("layanan gambar tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes*2))
	if err != nil {
		return nil, fmt.Errorf("baca respons layanan gambar: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		msg := snippet(raw)
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			msg = innerMessage(e.Error.Message)
		}
		return nil, &ImageError{Status: resp.StatusCode, Message: msg}
	}

	var out struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Data) == 0 {
		return nil, fmt.Errorf("respons layanan gambar tidak dikenali: %s", snippet(raw))
	}
	d := out.Data[0]

	img := &GeneratedImage{RevisedPrompt: d.RevisedPrompt}
	switch {
	case d.B64JSON != "":
		if img.Data, err = base64.StdEncoding.DecodeString(d.B64JSON); err != nil {
			return nil, fmt.Errorf("gambar base64 rusak: %w", err)
		}
	case d.URL != "":
		if img.Data, err = c.download(ctx, d.URL); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("layanan gambar tidak mengembalikan gambar")
	}
	if len(img.Data) > maxImageBytes {
		return nil, errors.New("gambar terlalu besar")
	}

	img.MIME = http.DetectContentType(img.Data)
	if !strings.HasPrefix(img.MIME, "image/") {
		return nil, fmt.Errorf("data dari layanan gambar bukan gambar (%s)", img.MIME)
	}
	return img, nil
}

// innerMessage mengambil pesan inti dari error provider yang dibungkus 9Router,
// mis. `[antigravity/x] [503]: {"error": {"message": "No capacity ..."}}`
// menjadi `No capacity ...`.
func innerMessage(msg string) string {
	if i := strings.Index(msg, "{"); i >= 0 {
		var inner struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		body := msg[i:]
		if j := strings.LastIndex(body, "}"); j >= 0 {
			body = body[:j+1]
		}
		if json.Unmarshal([]byte(body), &inner) == nil && inner.Error.Message != "" {
			return inner.Error.Message
		}
	}
	if i := strings.Index(msg, "\n"); i > 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}

func (c *ImageClient) download(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unduh gambar gagal: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("unduh gambar gagal: status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
}
