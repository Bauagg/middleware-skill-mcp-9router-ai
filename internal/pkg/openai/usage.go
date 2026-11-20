// Package openai berisi bantuan membaca respons berformat OpenAI (yang juga
// dipakai 9Router), tanpa mengubah respons itu.
package openai

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	maxJSONBytes = 4 << 20 // batas respons non-stream yang ditampung untuk dibaca
	maxLineBytes = 1 << 20 // batas satu baris SSE
)

// Usage adalah pemakaian token yang dilaporkan provider.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// CachedTokens adalah token input yang dilayani dari prompt cache provider
	// (0 bila tidak ada atau tidak dilaporkan).
	CachedTokens int
}

// rawUsage adalah bentuk field "usage" di JSON. Nama field token cache
// berbeda antar provider, jadi beberapa varian dibaca sekaligus.
type rawUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"` // OpenAI
	} `json:"prompt_tokens_details"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"` // DeepSeek
	CacheReadInputTokens int `json:"cache_read_input_tokens"` // gaya Anthropic
}

// toUsage mengubah ke Usage; nil bila kosong (artinya tidak dilaporkan).
func (r *rawUsage) toUsage() *Usage {
	if r == nil {
		return nil
	}

	u := &Usage{PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens, TotalTokens: r.TotalTokens}
	switch {
	case r.PromptTokensDetails != nil && r.PromptTokensDetails.CachedTokens > 0:
		u.CachedTokens = r.PromptTokensDetails.CachedTokens
	case r.PromptCacheHitTokens > 0:
		u.CachedTokens = r.PromptCacheHitTokens
	case r.CacheReadInputTokens > 0:
		u.CachedTokens = r.CacheReadInputTokens
	}

	if *u == (Usage{}) {
		return nil
	}
	return u
}

type chunk struct {
	Usage *rawUsage `json:"usage"`
}

// UsageSniffer membaca field "usage" dari byte respons yang sedang mengalir,
// tanpa mengubah atau menunda respons. Alirkan semua byte respons ke Write,
// lalu panggil Usage setelah respons selesai.
//
// Mendukung respons JSON biasa (ditampung sampai 4 MB) dan SSE (dibaca per
// baris). Pada streaming, provider hanya mengirim usage bila client meminta
// stream_options.include_usage; bila tidak, Usage mengembalikan nil.
type UsageSniffer struct {
	sse bool

	// mode SSE
	line     []byte
	skipLine bool // baris terlalu panjang, abaikan sampai baris berikutnya
	usage    *Usage

	// mode JSON
	buf      bytes.Buffer
	overflow bool
}

// NewUsageSniffer membuat sniffer sesuai Content-Type respons.
func NewUsageSniffer(contentType string) *UsageSniffer {
	return &UsageSniffer{sse: strings.HasPrefix(strings.ToLower(contentType), "text/event-stream")}
}

// Write memenuhi io.Writer; selalu sukses supaya tidak pernah mengganggu aliran.
func (s *UsageSniffer) Write(p []byte) (int, error) {
	n := len(p)

	if !s.sse {
		if !s.overflow {
			if s.buf.Len()+n > maxJSONBytes {
				s.overflow = true
				s.buf.Reset()
			} else {
				s.buf.Write(p)
			}
		}
		return n, nil
	}

	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.appendLine(p)
			break
		}
		s.appendLine(p[:i])
		s.endLine()
		p = p[i+1:]
	}
	return n, nil
}

// Usage mengembalikan pemakaian token yang terbaca, atau nil bila provider
// tidak melaporkannya. Aman dipanggil setelah semua byte ditulis.
func (s *UsageSniffer) Usage() *Usage {
	if s.sse {
		s.endLine() // baris terakhir yang tidak diakhiri newline
		return s.usage
	}

	if s.overflow || s.buf.Len() == 0 {
		return nil
	}
	var c chunk
	if json.Unmarshal(s.buf.Bytes(), &c) != nil {
		return nil
	}
	return c.Usage.toUsage()
}

func (s *UsageSniffer) appendLine(b []byte) {
	if s.skipLine {
		return
	}
	if len(s.line)+len(b) > maxLineBytes {
		s.skipLine = true
		s.line = s.line[:0]
		return
	}
	s.line = append(s.line, b...)
}

func (s *UsageSniffer) endLine() {
	if !s.skipLine {
		s.parseLine(s.line)
	}
	s.line = s.line[:0]
	s.skipLine = false
}

// parseLine membaca satu baris SSE ("data: {...}"); usage terakhir yang
// ditemukan yang dipakai.
func (s *UsageSniffer) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}

	payload := bytes.TrimSpace(line[len("data:"):])
	if !bytes.Contains(payload, []byte(`"usage"`)) { // jalur cepat: sebagian besar chunk tidak memuatnya
		return
	}

	var c chunk
	if json.Unmarshal(payload, &c) != nil {
		return
	}
	if u := c.Usage.toUsage(); u != nil {
		s.usage = u
	}
}
