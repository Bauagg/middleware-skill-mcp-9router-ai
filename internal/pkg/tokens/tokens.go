// Package tokens memperkirakan jumlah token tanpa tokenizer sungguhan.
package tokens

import (
	"strings"
	"unicode/utf8"
)

// Estimate memperkirakan jumlah token sebuah teks: sekitar 1 token per 4
// karakter, dibulatkan ke atas. Ini hanya perkiraan kasar (selisihnya bisa
// ±25% tergantung bahasa dan model); cukup untuk budget dan pelaporan,
// bukan untuk penagihan.
func Estimate(s string) int {
	return (utf8.RuneCountInString(s) + 3) / 4
}

// Truncate memotong s sampai perkiraan tokennya tidak melebihi budget,
// sebisa mungkin di batas paragraf (atau baris) supaya kalimat tidak terpotong
// di tengah. Mengembalikan true bila teks dipotong.
func Truncate(s string, budget int) (string, bool) {
	if budget < 1 {
		budget = 1
	}
	if Estimate(s) <= budget {
		return s, false
	}

	runes := []rune(s)
	if len(runes) > budget*4 {
		runes = runes[:budget*4]
	}
	cut := string(runes)

	// cari batas paragraf (atau baris) terakhir, asal tidak membuang lebih dari separuh
	if i := strings.LastIndex(cut, "\n\n"); i > len(cut)/2 {
		cut = cut[:i]
	} else if i := strings.LastIndexByte(cut, '\n'); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \n"), true
}
