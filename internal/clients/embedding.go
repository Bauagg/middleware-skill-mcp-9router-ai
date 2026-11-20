package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// embedBatch adalah jumlah teks per satu panggilan embedding.
const embedBatch = 64

// Embedder adalah klien layanan embedding berformat OpenAI
// (POST {base}/embeddings). Format ini didukung 9Router (bila punya provider
// embedding), Ollama, LM Studio, dan text-embeddings-inference, jadi pindah
// layanan cukup mengganti EMBEDDING_BASE_URL dan EMBEDDING_MODEL.
type Embedder struct {
	url    string
	apiKey string
	model  string
	http   *http.Client
}

// NewEmbedder membuat klien embedding.
func NewEmbedder(base *url.URL, apiKey, model string) *Embedder {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second}).DialContext

	return &Embedder{
		url:    strings.TrimRight(base.String(), "/") + "/embeddings",
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Transport: transport},
	}
}

// Model mengembalikan nama model embedding (dipakai sebagai bagian kunci
// cache vektor, supaya ganti model otomatis menghitung ulang).
func (e *Embedder) Model() string { return e.model }

// Embed mengubah teks menjadi vektor, urutannya sama dengan inputs. Batas
// waktu diatur lewat ctx.
func (e *Embedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += embedBatch {
		end := min(start+embedBatch, len(inputs))
		vecs, err := e.embedBatch(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *Embedder) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": e.model, "input": inputs})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("layanan embedding tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("baca respons embedding: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("layanan embedding menjawab %d: %s", resp.StatusCode, snippet(raw))
	}

	var res struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("respons embedding tidak dikenali: %s", snippet(raw))
	}
	if len(res.Data) != len(inputs) {
		return nil, fmt.Errorf("layanan embedding mengembalikan %d vektor untuk %d teks", len(res.Data), len(inputs))
	}

	sort.Slice(res.Data, func(i, j int) bool { return res.Data[i].Index < res.Data[j].Index })
	vecs := make([][]float32, len(res.Data))
	for i, d := range res.Data {
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("layanan embedding mengembalikan vektor kosong")
		}
		vecs[i] = d.Embedding
	}
	return vecs, nil
}
