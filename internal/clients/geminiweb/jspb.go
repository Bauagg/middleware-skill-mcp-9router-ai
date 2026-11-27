package geminiweb

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

// Gemini Web menjawab dalam JSPB: array JSON bersarang yang isinya dikenali
// dari posisi, bukan nama field. Fungsi di file ini membaca posisi itu dengan
// aman (tanpa panic bila bentuknya berubah).

// at menelusuri v mengikuti posisi array (int) atau kunci objek (string).
// Hasilnya nil bila jalurnya tidak ada.
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case int:
			a, ok := v.([]any)
			if !ok || k < 0 || k >= len(a) {
				return nil
			}
			v = a[k]
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		}
	}
	return v
}

// str membaca string di jalur itu ("" bila tidak ada atau bukan string).
func str(v any, path ...any) string {
	s, _ := at(v, path...).(string)
	return s
}

// list membaca array di jalur itu (nil bila tidak ada atau bukan array).
func list(v any, path ...any) []any {
	a, _ := at(v, path...).([]any)
	return a
}

// field membaca field nomor idx sebuah pesan JSPB. Field bernomor kecil ada
// di posisinya; field bernomor besar dikumpulkan di objek pada elemen
// terakhir dengan kunci nomor field + 1.
func field(v any, idx int) any {
	a, ok := v.([]any)
	if !ok || len(a) == 0 {
		return nil
	}
	var out any
	if idx < len(a) {
		out = a[idx]
	}
	if _, isBundle := out.(map[string]any); out == nil || isBundle || isEmpty(out) {
		if bundle, ok := a[len(a)-1].(map[string]any); ok {
			out = bundle[strconv.Itoa(idx+1)]
		}
	}
	if isEmpty(out) {
		return nil
	}
	return out
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// frames memecah respons Google menjadi daftar envelope. Bentuknya:
//
//	)]}'
//	<panjang>
//	[[...],[...]]
//	<panjang>
//	[[...]]
//
// Panjang dihitung dalam satuan UTF-16 (gaya JavaScript), jadi tidak dipakai:
// setiap blok JSON dibaca utuh dengan decoder.
func frames(body string) []any {
	body = strings.TrimPrefix(strings.TrimLeftFunc(body, unicode.IsSpace), ")]}'")
	var out []any
	for {
		body = strings.TrimLeftFunc(body, unicode.IsSpace)
		i := 0
		for i < len(body) && body[i] >= '0' && body[i] <= '9' {
			i++
		}
		if i == 0 {
			return out
		}
		dec := json.NewDecoder(strings.NewReader(body[i:]))
		var v any
		if err := dec.Decode(&v); err != nil {
			return out
		}
		if a, ok := v.([]any); ok {
			out = append(out, a...)
		} else {
			out = append(out, v)
		}
		body = body[i+int(dec.InputOffset()):]
	}
}

// innerJSON mengurai string JSON yang tertanam di dalam envelope.
func innerJSON(s string) any {
	if s == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return nil
	}
	return v
}
