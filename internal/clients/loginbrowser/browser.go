// Package loginbrowser mengendalikan Chromium di container browser login
// (docker/login-browser): user login ke situs AI lewat layar noVNC di FE, lalu
// middleware mengambil cookie sesinya.
package loginbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Container browser login (docker/login-browser). Di Docker, middleware
// menjangkaunya lewat nama service; saat middleware dijalankan di luar Docker
// (go run di laptop), port-nya dibuka di 127.0.0.1.
const (
	browserHost = "login-browser"
	cdpPort     = 9333 // DevTools
	vncPort     = 6090 // websockify untuk noVNC
)

// ErrBrowserDown berarti container browser login tidak berjalan.
var ErrBrowserDown = errors.New("browser login belum berjalan: jalankan service login-browser (docker compose up -d login-browser)")

// Browser mengendalikan Chromium di container browser login lewat DevTools:
// membuka jendela login di browser context sekali pakai, lalu mengambil
// cookie situsnya setelah user login lewat layar noVNC.
type Browser struct{}

// host mengembalikan IP container. DevTools hanya menerima Host berupa IP atau
// localhost, jadi nama service di-resolve dulu.
func (b *Browser) host(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if ips, err := net.DefaultResolver.LookupIPAddr(ctx, browserHost); err == nil && len(ips) > 0 {
		return ips[0].IP.String()
	}
	return "127.0.0.1"
}

// VNCAddr mengembalikan host:port websockify untuk layar login.
func (b *Browser) VNCAddr(ctx context.Context) string {
	return net.JoinHostPort(b.host(ctx), strconv.Itoa(vncPort))
}

// Available melaporkan apakah browser login bisa dihubungi.
func (b *Browser) Available(ctx context.Context) bool {
	_, err := b.debuggerURL(ctx)
	return err == nil
}

func (b *Browser) debuggerURL(ctx context.Context) (string, error) {
	addr := net.JoinHostPort(b.host(ctx), strconv.Itoa(cdpPort))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", ErrBrowserDown
	}
	defer resp.Body.Close()
	var v struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v); err != nil || v.URL == "" {
		return "", ErrBrowserDown
	}
	// URL-nya berisi alamat internal container (localhost:9222): arahkan ke socat
	u, err := url.Parse(v.URL)
	if err != nil {
		return "", err
	}
	u.Host = addr
	return u.String(), nil
}

// call menjalankan satu perintah DevTools di level browser.
func (b *Browser) call(ctx context.Context, method string, params, out any) error {
	wsURL, err := b.debuggerURL(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("DevTools tidak dapat dihubungi: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)

	msg, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": params})
	if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
		return err
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("DevTools %s: %w", method, err)
		}
		var r struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &r) != nil || r.ID != 1 {
			continue // event, bukan jawaban
		}
		if r.Error != nil {
			return fmt.Errorf("DevTools %s: %s", method, r.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	}
}

// StartLogin membuka halaman login di browser context baru (seperti jendela
// Incognito) dan menampilkannya di layar. Hasilnya id context.
func (b *Browser) StartLogin(ctx context.Context, loginURL string) (string, error) {
	var bc struct {
		ID string `json:"browserContextId"`
	}
	if err := b.call(ctx, "Target.createBrowserContext", map[string]any{}, &bc); err != nil {
		return "", err
	}
	var t struct {
		ID string `json:"targetId"`
	}
	if err := b.call(ctx, "Target.createTarget", map[string]any{
		"url": loginURL, "browserContextId": bc.ID, "newWindow": true,
	}, &t); err != nil {
		_ = b.Dispose(ctx, bc.ID)
		return "", err
	}
	_ = b.call(ctx, "Target.activateTarget", map[string]any{"targetId": t.ID}, nil)
	return bc.ID, nil
}

// Cookies mengambil cookie dari browser context login, hanya untuk domain
// yang disebut (dan subdomainnya). Bila nama yang sama ada di beberapa
// domain, cookie di domain yang persis disebut menang atas subdomain.
func (b *Browser) Cookies(ctx context.Context, contextID string, domains []string) (map[string]string, error) {
	var r struct {
		Cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		} `json:"cookies"`
	}
	if err := b.call(ctx, "Storage.getCookies", map[string]any{"browserContextId": contextID}, &r); err != nil {
		return nil, err
	}
	out := map[string]string{}
	exact := map[string]bool{}
	for _, c := range r.Cookies {
		d := strings.TrimPrefix(c.Domain, ".")
		top, ok := matchDomain(d, domains)
		if c.Value == "" || !ok {
			continue
		}
		if _, seen := out[c.Name]; !seen || (top && !exact[c.Name]) {
			out[c.Name], exact[c.Name] = c.Value, top
		}
	}
	return out, nil
}

// matchDomain melaporkan apakah host termasuk salah satu domain, dan apakah
// persis domain itu (bukan subdomain).
func matchDomain(host string, domains []string) (exact, ok bool) {
	for _, d := range domains {
		if host == d {
			return true, true
		}
		if strings.HasSuffix(host, "."+d) {
			ok = true
		}
	}
	return false, ok
}

// Dispose menutup browser context login: browser melupakan sesinya, supaya
// sesi itu hanya dipakai (dan diperbarui) oleh middleware.
func (b *Browser) Dispose(ctx context.Context, contextID string) error {
	return b.call(ctx, "Target.disposeBrowserContext", map[string]any{"browserContextId": contextID}, nil)
}
