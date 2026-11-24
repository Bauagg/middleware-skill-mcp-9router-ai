package clients

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// ErrNoRouterURL berarti panggilan ke 9Router tidak membawa alamat milik user.
var ErrNoRouterURL = errors.New("alamat 9Router milik user tidak ada (header X-Router-URL)")

// ParseRouterURL memvalidasi alamat dasar 9Router kiriman user dan
// mengembalikan bentuk rapinya (tanpa "/" di akhir), mis. https://host/v1.
func ParseRouterURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrNoRouterURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("alamat 9Router harus berbentuk http(s)://host/v1, bukan %q", raw)
	}
	// kredensial di URL, query, dan fragment tidak punya arti untuk alamat dasar
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("alamat 9Router tidak boleh berisi user:password, query, atau fragment")
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/"), nil
}

// routerBase mengambil alamat dasar 9Router milik user dari context.
func routerBase(ctx context.Context) (string, error) {
	if b := userctx.RouterURL(ctx); b != "" {
		return b, nil
	}
	return "", ErrNoRouterURL
}

// cgnat adalah rentang 100.64.0.0/10 (jaringan internal penyedia/VPN).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// blockedIP melaporkan alamat yang tidak boleh dituju atas perintah user:
// loopback, jaringan privat, link-local (termasuk metadata cloud 169.254.169.254),
// dan sejenisnya. Tanpa ini, alamat 9Router kiriman client bisa dipakai untuk
// mengakses layanan internal server (Redis, PostgreSQL, dll.).
func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip)
}

// newRouterTransport membuat transport untuk semua panggilan ke 9Router user.
// IP tujuan diperiksa setelah DNS di-resolve (saat dial), jadi nama domain
// yang mengarah ke IP internal juga ditolak. allowPrivate=true mematikan
// pemeriksaan ini (mis. 9Router di jaringan lokal saat pengembangan).
func newRouterTransport(allowPrivate bool) *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("alamat tujuan tidak dikenali: %s", address)
			}
			if blockedIP(ap.Addr()) {
				return fmt.Errorf("alamat 9Router mengarah ke jaringan internal (%s), ditolak", ap.Addr())
			}
			return nil
		}
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil // jangan lewat proxy sistem: pemeriksaan IP di atas harus berlaku
	t.DialContext = dialer.DialContext
	t.TLSHandshakeTimeout = 10 * time.Second
	t.MaxIdleConnsPerHost = 32
	return t
}
