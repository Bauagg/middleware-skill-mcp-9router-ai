package geminiweb

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

const (
	maxImageBytes = 30 << 20
	maxVideoBytes = 300 << 20

	rpcFullImage = "c8o8Fe" // URL gambar ukuran penuh
)

// DownloadImage mengunduh gambar ukuran penuh. URL preview di jawaban tidak
// bisa diunduh langsung (CDN menjawab 403); URL ukuran penuh diminta lewat
// RPC, lalu rantai lompatan CDN-nya diikuti sampai berisi gambar.
func (s *Session) DownloadImage(ctx context.Context, r *Reply, img GeneratedImage) ([]byte, error) {
	start := img.URL + "=s2048-rj"
	if full, err := s.fullImageURL(ctx, r, img); err == nil && full != "" {
		start = full + "=d-I?alr=yes"
	}
	return s.followMedia(ctx, start, maxImageBytes, "image/")
}

func (s *Session) fullImageURL(ctx context.Context, r *Reply, img GeneratedImage) (string, error) {
	if r.cid == "" || r.rid == "" || r.rcid == "" || img.ID == "" {
		return "", errors.New("pengenal gambar tidak lengkap")
	}
	payload, _ := json.Marshal([]any{
		[]any{[]any{nil, nil, nil, []any{nil, nil, nil, nil, nil, ""}}, []any{img.ID, 0}, nil, []any{19, ""}, nil, nil, nil, nil, nil, ""},
		[]any{r.rid, r.rcid, r.cid, nil, ""}, 1, 0, 1,
	})
	out, err := s.batch(ctx, rpcFullImage, string(payload))
	if err != nil {
		return "", err
	}
	return str(innerJSON(out), 0), nil
}

// batch menjalankan satu RPC batchexecute dan mengembalikan isi jawabannya.
func (s *Session) batch(ctx context.Context, rpcID, payload string) (string, error) {
	q, token := s.rpcParams()
	q.Set("rpcids", rpcID)
	q.Set("source-path", "/app")
	freq, _ := json.Marshal([]any{[]any{[]any{rpcID, payload, nil, "generic"}}})
	form := url.Values{"at": {token}, "f.req": {string(freq)}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlBatch+"?"+q.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	s.decorate(req, map[string]string{
		"Content-Type":              "application/x-www-form-urlencoded;charset=utf-8",
		"Origin":                    "https://gemini.google.com",
		"Referer":                   "https://gemini.google.com/",
		"X-Same-Domain":             "1",
		"x-goog-ext-525001261-jspb": `[1,null,null,null,null,null,null,null,[4,5,6,8],null,null,null,null,null,null,null,"` + newUUID() + `"]`,
		"x-goog-ext-73010989-jspb":  "[0]",
	})
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	s.jar.absorb(resp)
	if err := statusError(resp); err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	for _, env := range frames(string(body)) {
		if str(env, 0) == "wrb.fr" && str(env, 1) == rpcID {
			return str(env, 2), nil
		}
	}
	return "", fmt.Errorf("RPC %s tidak dijawab", rpcID)
}

// followMedia mengikuti rantai CDN Google: setiap lompatan menjawab URL
// berikutnya sebagai teks, sampai yang terakhir berisi file. Lompatan
// gg-dl yang ditolak dicoba lagi dengan akhiran =d-I.
func (s *Session) followMedia(ctx context.Context, start string, limit int64, want string) ([]byte, error) {
	next := start
	seen := map[string]bool{}
	for hop := 0; hop < 8 && next != "" && !seen[next]; hop++ {
		seen[next] = true
		cands := []string{next}
		if strings.Contains(next, "gg-dl/") && !strings.Contains(next, "=d-I") {
			cands = append(cands, next+"=d-I?alr=yes")
		}
		next = ""
		var lastErr error
		for _, u := range cands {
			status, ctype, data, err := s.fetchMedia(ctx, u, limit)
			if err != nil {
				lastErr = err
				continue
			}
			if status != http.StatusOK {
				lastErr = fmt.Errorf("CDN Google menjawab %d", status)
				continue
			}
			if strings.HasPrefix(ctype, want) {
				return data, nil
			}
			if t := strings.TrimSpace(string(data)); strings.HasPrefix(t, "http") {
				next = strings.Fields(t)[0]
				break
			}
			lastErr = fmt.Errorf("CDN Google mengirim %s, bukan %s", ctype, want)
		}
		if next == "" && lastErr != nil {
			return nil, fmt.Errorf("unduh dari Gemini gagal: %w", lastErr)
		}
	}
	return nil, errors.New("unduh dari Gemini gagal: rantai URL tidak berujung ke file")
}

// fetchMedia mengambil satu URL CDN dengan cookie login saja (ke semua host).
func (s *Session) fetchMedia(ctx context.Context, u string, limit int64) (int, string, []byte, error) {
	cookie := s.jar.header(downloadCookies)
	hc := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("terlalu banyak redirect")
		}
		req.Header.Set("Cookie", cookie)
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Origin", "https://gemini.google.com")
	req.Header.Set("Referer", "https://gemini.google.com/")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, "", nil, err
	}
	if int64(len(data)) > limit {
		return 0, "", nil, errors.New("file dari Gemini terlalu besar")
	}
	ctype := http.DetectContentType(data)
	if h := resp.Header.Get("Content-Type"); strings.HasPrefix(h, "video/") {
		ctype = h // DetectContentType tidak selalu mengenali mp4
	}
	return resp.StatusCode, ctype, data, nil
}

// DownloadVideo mengunduh video. Selama Google masih menyelesaikannya, CDN
// menjawab 206; unduhan diulang tiap 10 detik sampai ctx habis.
func (s *Session) DownloadVideo(ctx context.Context, v GeneratedVideo) ([]byte, error) {
	u := v.URL
	for hops := 0; ; {
		status, ctype, data, err := s.fetchMedia(ctx, u, maxVideoBytes)
		text := strings.TrimSpace(string(data))
		switch {
		case err != nil:
			return nil, fmt.Errorf("unduh video gagal: %w", err)
		case status == http.StatusOK && (strings.HasPrefix(ctype, "video/") || ctype == "application/octet-stream"):
			return data, nil
		case status == http.StatusOK && strings.HasPrefix(text, "http") && hops < 8:
			// lompatan CDN seperti gambar: isinya URL berikutnya
			u, hops = strings.Fields(text)[0], hops+1
			continue
		case status == http.StatusOK:
			return nil, fmt.Errorf("unduh video gagal: CDN mengirim %s", ctype)
		case status != http.StatusPartialContent:
			return nil, fmt.Errorf("unduh video gagal: CDN Google menjawab %d", status)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}
