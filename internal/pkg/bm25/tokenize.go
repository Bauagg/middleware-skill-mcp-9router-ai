package bm25

import (
	"strings"
	"unicode"
)

// stopwords dibuang dari dokumen dan query. Isinya kata umum Indonesia dan
// Inggris, boilerplate deskripsi skill ("use when the user says"), serta sapaan
// dan ucapan terima kasih, supaya pesan seperti itu tidak menghasilkan skor.
var stopwords = func() map[string]bool {
	words := `
		yang dan di ke dari untuk dengan atau ini itu saya aku kamu anda kami kita mereka ada adalah akan
		bisa dapat tolong mohon mau ingin buat buatkan bikin bikinin gimana bagaimana apa apakah dong deh sih
		juga sudah belum masih lagi saja aja nih tuh kok lah kah
		the an and or to of in on for with is are be am was were i me my you your it its this that these those
		can could would should please how what do does did use when user users says say help need want
		halo hai hi hello hey selamat pagi siang sore malam terima kasih makasih thanks thank oke ok okay ya yes no tidak
	`
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}()

// Tokenize memecah teks menjadi token huruf/angka huruf kecil, membuang
// stopword, lalu menyederhanakan bentuk kata (lihat stem).
func Tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) < 2 || stopwords[f] {
			continue
		}
		tokens = append(tokens, stem(f))
	}
	return tokens
}

// stem adalah penyederhanaan ringan, bukan stemmer lengkap: membuang akhiran
// -nya/-lah/-kah/-pun (Indonesia) dan "s" jamak (Inggris). Diterapkan sama
// pada dokumen dan query, jadi yang penting konsisten, bukan benar secara bahasa.
func stem(w string) string {
	for _, suf := range []string{"nya", "lah", "kah", "pun"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 4 {
			w = strings.TrimSuffix(w, suf)
			break
		}
	}
	if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
		w = w[:len(w)-1]
	}
	return w
}
