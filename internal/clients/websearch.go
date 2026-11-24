package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearchResult adalah satu hasil pencarian web.
type SearchResult struct {
	Title     string
	URL       string
	Snippet   string
	Engine    string
	Published string
}

// SearXNG memanggil API JSON mesin pencari SearXNG (self-hosted).
type SearXNG struct {
	base string
	http *http.Client
}

// NewSearXNG membuat klien. base adalah alamat SearXNG, mis. http://searxng:8080.
func NewSearXNG(base string) *SearXNG {
	return &SearXNG{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

// Search mencari query dan mengembalikan maksimal max hasil.
func (s *SearXNG) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	q := url.Values{"q": {query}, "format": {"json"}, "safesearch": {"0"}, "pageno": {"1"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mesin pencari tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("baca hasil pencarian: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, errors.New("SearXNG menolak format JSON; aktifkan 'json' di search.formats pada settings.yml")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("SearXNG membatasi permintaan (limiter); matikan server.limiter di settings.yml")
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("SearXNG menjawab %d: %s", resp.StatusCode, snippet(raw))
	}

	var out struct {
		Results []struct {
			URL           string `json:"url"`
			Title         string `json:"title"`
			Content       string `json:"content"`
			Engine        string `json:"engine"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("hasil pencarian tidak dikenali: %w", err)
	}

	seen := map[string]bool{}
	var res []SearchResult
	for _, r := range out.Results {
		if r.URL == "" || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		res = append(res, SearchResult{Title: strings.TrimSpace(r.Title), URL: r.URL,
			Snippet: strings.TrimSpace(r.Content), Engine: r.Engine, Published: r.PublishedDate})
		if max > 0 && len(res) == max {
			break
		}
	}
	return res, nil
}
