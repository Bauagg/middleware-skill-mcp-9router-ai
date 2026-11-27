// Package cookies membaca cookie login yang ditempel user dari browser.
package cookies

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrEmpty berarti tidak ada cookie yang terbaca.
var ErrEmpty = errors.New("tidak ada cookie yang terbaca: salin semua cookie situsnya saat sudah login")

// Parse membaca cookie dalam salah satu bentuk:
//   - header Cookie dari DevTools: "SID=...; __Secure-1PSID=...; ..."
//   - ekspor JSON ekstensi browser: [{"name": "...", "value": "..."}, ...]
//     atau objek {"nama": "nilai"}
//   - file cookies.txt (format Netscape)
func Parse(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "Cookie:"), "cookie:")
	raw = strings.TrimSpace(raw)
	out := map[string]string{}

	switch {
	case strings.HasPrefix(raw, "["):
		var arr []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(raw), &arr); err != nil {
			return nil, errors.New("JSON cookie tidak valid: " + err.Error())
		}
		for _, c := range arr {
			put(out, c.Name, c.Value)
		}
	case strings.HasPrefix(raw, "{"):
		var obj map[string]string
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return nil, errors.New("JSON cookie tidak valid: " + err.Error())
		}
		for k, v := range obj {
			put(out, k, v)
		}
	case strings.Contains(raw, "\t"):
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimPrefix(strings.TrimSpace(line), "#HttpOnly_")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if f := strings.Split(line, "\t"); len(f) >= 7 {
				put(out, f[5], f[6])
			}
		}
	default:
		for _, part := range strings.Split(raw, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
				put(out, k, v)
			}
		}
	}

	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

func put(m map[string]string, k, v string) {
	if k, v = strings.TrimSpace(k), strings.TrimSpace(v); k != "" && v != "" {
		m[k] = v
	}
}

// HasAny melaporkan apakah salah satu nama cookie ada (nama berakhiran "*"
// dicocokkan sebagai awalan, untuk cookie yang dipecah, mis. session-token.0).
func HasAny(m map[string]string, names []string) bool {
	for _, n := range names {
		if p, ok := strings.CutSuffix(n, "*"); ok {
			for k, v := range m {
				if strings.HasPrefix(k, p) && v != "" {
					return true
				}
			}
		} else if m[n] != "" {
			return true
		}
	}
	return false
}
