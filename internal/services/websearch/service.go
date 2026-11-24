// Package websearch membuat chat bisa mencari di internet lewat SearXNG
// (mesin pencari self-hosted, tanpa API key). Model mendapat tool bawaan
// "web_search" bila pesan tampak butuh info dari web: mencari skill, harga
// atau iklan pesaing, tren, berita terbaru, referensi, dan sejenisnya.
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/llmtool"
)

// ToolName adalah nama tool yang dilihat model.
const ToolName = "web_search"

const (
	snippetRunes = 300
	maxQueryLen  = 300
)

// intent menandai pesan yang butuh info dari internet (dicocokkan sebagai
// awalan kata, mis. "cari" juga cocok "carikan", "mencari" tidak).
var intent = []string{
	"cari", "telusur", "search", "googl", "browsing", "internet", "online", "web",
	"terbaru", "terkini", "berita", "news", "tren", "trend", "viral", "update",
	"pesaing", "kompetitor", "competitor", "referensi", "sumber", "riset", "research", "link",
}

// Searcher mencari di web; clients.SearXNG memenuhinya.
type Searcher interface {
	Search(ctx context.Context, query string, max int) ([]clients.SearchResult, error)
}

// Service menyediakan tool web_search.
type Service struct {
	search     Searcher // nil = web search dimatikan
	maxResults int
	timeout    time.Duration
	spec       json.RawMessage
}

// NewService membuat Service. search nil berarti WEB_SEARCH_URL kosong.
func NewService(search Searcher, maxResults int, timeout time.Duration) *Service {
	return &Service{search: search, maxResults: maxResults, timeout: timeout, spec: llmtool.Function(ToolName,
		"Cari informasi terbaru di internet (mesin pencari). Pakai bila jawaban butuh data dari web: mencari skill, "+
			"harga/iklan/strategi pesaing, tren, berita, referensi, atau fakta yang mungkin berubah. "+
			"Tulis query singkat dan spesifik; boleh dipanggil beberapa kali dengan query berbeda. "+
			"Sebutkan sumbernya ke user sebagai link markdown.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "kata kunci pencarian, mis. \"meta ads skill SKILL.md github\""},
			},
			"required": []string{"query"},
		})}
}

// Enabled melaporkan apakah web search aktif.
func (s *Service) Enabled() bool { return s.search != nil }

// Select menawarkan web_search bila pesan tampak butuh info dari internet.
func (s *Service) Select(_ context.Context, query string) []llmtool.Spec {
	if s.search == nil || !needsWeb(query) {
		return nil
	}
	return []llmtool.Spec{{Name: ToolName, Source: "web", Definition: s.spec}}
}

// Has melaporkan apakah tool ini milik web search.
func (s *Service) Has(_ context.Context, name string) bool {
	return s.search != nil && name == ToolName
}

// Call menjalankan pencarian dan mengembalikan hasilnya sebagai teks.
func (s *Service) Call(ctx context.Context, _ string, args string) llmtool.Result {
	var a struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &a); err != nil || strings.TrimSpace(a.Query) == "" {
		return llmtool.Result{Text: `argumen tidak valid: kirim {"query": "..."}`, IsError: true}
	}
	q := strings.TrimSpace(a.Query)
	if r := []rune(q); len(r) > maxQueryLen {
		q = string(r[:maxQueryLen])
	}

	results, err := s.Search(ctx, q)
	if err != nil {
		return llmtool.Result{Text: "pencarian web gagal: " + err.Error(), IsError: true}
	}
	if len(results) == 0 {
		return llmtool.Result{Text: "Tidak ada hasil untuk \"" + q + "\". Coba kata kunci lain yang lebih umum atau dalam bahasa Inggris."}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Hasil pencarian web untuk \"%s\":\n", q)
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			b.WriteString("   " + clip(r.Snippet, snippetRunes) + "\n")
		}
		if r.Published != "" {
			b.WriteString("   (terbit " + r.Published + ")\n")
		}
	}
	b.WriteString("\nIni hanya cuplikan; jangan mengarang isi halaman yang tidak terlihat. Cantumkan link sumber saat memakai informasinya.")
	return llmtool.Result{Text: b.String()}
}

// Search menjalankan pencarian (dipakai tool dan endpoint uji).
func (s *Service) Search(ctx context.Context, query string) ([]clients.SearchResult, error) {
	if s.search == nil {
		return nil, fmt.Errorf("web search belum diaktifkan (WEB_SEARCH_URL kosong)")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	start := time.Now()
	res, err := s.search.Search(ctx, query, s.maxResults)
	if err != nil {
		logger.Warn("web search gagal", zap.String("query", query), zap.Error(err))
		return nil, err
	}
	logger.Info("web search", zap.String("query", query), zap.Int("results", len(res)), zap.Duration("latency", time.Since(start)))
	return res, nil
}

// needsWeb melaporkan apakah pesan tampak butuh info dari internet.
func needsWeb(text string) bool {
	low := strings.ToLower(text)
	if strings.Contains(low, "http://") || strings.Contains(low, "https://") {
		return false // link sudah diberikan: tool lain (impor skill, MCP) yang menangani
	}
	for _, w := range strings.FieldsFunc(low, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		for _, p := range intent {
			if strings.HasPrefix(w, p) {
				return true
			}
		}
	}
	return false
}

func clip(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}
