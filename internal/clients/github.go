package clients

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxRepoZipBytes membatasi ukuran arsip repo yang diunduh.
const maxRepoZipBytes = 30 << 20

// GitHub mengunduh isi repo publik GitHub sebagai arsip zip.
type GitHub struct {
	http *http.Client
}

// NewGitHub membuat klien GitHub (tanpa token: hanya repo publik).
func NewGitHub() *GitHub {
	return &GitHub{http: &http.Client{Timeout: 90 * time.Second}}
}

// DownloadZip mengunduh arsip repo owner/repo pada ref (cabang, tag, atau
// commit; kosong = cabang utama).
func (g *GitHub) DownloadZip(ctx context.Context, owner, repo, ref string) ([]byte, error) {
	if ref == "" {
		ref = "HEAD"
	}
	u := fmt.Sprintf("https://github.com/%s/%s/archive/%s.zip",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "middleware-skill-ai")

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("repo atau cabang tidak ditemukan di GitHub (repo private tidak didukung)")
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("GitHub menjawab %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRepoZipBytes+1))
	if err != nil {
		return nil, fmt.Errorf("unduh arsip repo gagal: %w", err)
	}
	if len(data) > maxRepoZipBytes {
		return nil, errors.New("arsip repo terlalu besar (maks 30 MB)")
	}
	return data, nil
}

// CommitSHA mengambil SHA commit terbaru dari ref (kosong = cabang utama).
// SHA dipakai untuk mengunci versi kode: yang dijalankan persis yang diperiksa.
func (g *GitHub) CommitSHA(ctx context.Context, owner, repo, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "middleware-skill-ai")
	req.Header.Set("Accept", "application/vnd.github.sha")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitHub tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity:
		return "", errors.New("repo atau cabang tidak ditemukan di GitHub (repo private tidak didukung)")
	case resp.StatusCode/100 != 2:
		return "", fmt.Errorf("GitHub menjawab %d", resp.StatusCode)
	}
	sha := strings.TrimSpace(string(raw))
	if len(sha) != 40 {
		return "", errors.New("SHA commit dari GitHub tidak dikenali")
	}
	return sha, nil
}
