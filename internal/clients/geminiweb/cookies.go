package geminiweb

import (
	"net/http"
	"sort"
	"strings"
	"sync"
)

// downloadCookies adalah cookie login yang dikirim saat mengunduh gambar/video
// dari CDN Google. Cookie pelacak (NID, COMPASS, ...) membuat CDN menolak
// (403), jadi hanya cookie ini yang dikirim, ke setiap lompatan redirect.
var downloadCookies = map[string]bool{
	"__Secure-1PSID": true, "__Secure-1PSIDTS": true, "__Secure-1PSIDCC": true,
	"__Secure-3PSID": true, "__Secure-3PSIDTS": true, "__Secure-3PSIDCC": true, "__Secure-3PAPISID": true,
	"SAPISID": true, "APISID": true, "HSID": true, "SSID": true, "SID": true, "SIDCC": true,
}

// jar menyimpan cookie satu akun. Google memperbarui sebagian cookie lewat
// Set-Cookie di respons; perubahan itu ditandai supaya disimpan ulang.
type jar struct {
	mu      sync.Mutex
	cookies map[string]string
	changed bool
}

func newJar(c map[string]string) *jar {
	m := make(map[string]string, len(c))
	for k, v := range c {
		m[k] = v
	}
	return &jar{cookies: m}
}

// header menyusun isi header Cookie; only (bila tidak nil) membatasi nama cookie.
func (j *jar) header(only map[string]bool) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	names := make([]string, 0, len(j.cookies))
	for k := range j.cookies {
		if only == nil || only[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = k + "=" + j.cookies[k]
	}
	return strings.Join(parts, "; ")
}

// absorb menyerap Set-Cookie untuk domain Google dari sebuah respons.
func (j *jar) absorb(resp *http.Response) {
	if resp == nil {
		return
	}
	cs := resp.Cookies()
	if len(cs) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		d := strings.TrimPrefix(c.Domain, ".")
		if d != "" && d != "google.com" && !strings.HasSuffix(d, ".google.com") {
			continue
		}
		if c.MaxAge < 0 || c.Value == "" {
			continue // penghapusan cookie tidak diikuti: sesi lebih penting
		}
		if j.cookies[c.Name] != c.Value {
			j.cookies[c.Name] = c.Value
			j.changed = true
		}
	}
}

func (j *jar) get(name string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cookies[name]
}

// snapshot mengembalikan salinan cookie dan apakah berubah sejak snapshot terakhir.
func (j *jar) snapshot() (map[string]string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	m := make(map[string]string, len(j.cookies))
	for k, v := range j.cookies {
		m[k] = v
	}
	changed := j.changed
	j.changed = false
	return m, changed
}
