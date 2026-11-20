// Package modelbudget menentukan batas token skill per model. Model besar
// boleh menerima skill panjang; model kecil kurang patuh pada instruksi
// panjang dan lebih mahal per token, jadi dibatasi lebih ketat.
package modelbudget

import (
	"fmt"
	"strconv"
	"strings"
)

// Rule memetakan pola nama model ke budget token.
type Rule struct {
	Pattern string // huruf kecil; "*" cocok dengan urutan karakter apa pun
	Tokens  int
}

// Budgets adalah daftar aturan beserta budget bawaan.
type Budgets struct {
	// Default dipakai bila tidak ada aturan yang cocok. 0 = tanpa batas.
	Default int
	Rules   []Rule
}

// Parse membaca aturan dari teks "pola=token,pola=token", mis.
// "*mini*=800,*haiku*=800". Pola tidak membedakan huruf besar/kecil.
func Parse(spec string, def int) (Budgets, error) {
	b := Budgets{Default: def}

	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		pattern, value, ok := strings.Cut(part, "=")
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		tokens, err := strconv.Atoi(strings.TrimSpace(value))
		if !ok || pattern == "" || err != nil || tokens <= 0 {
			return Budgets{}, fmt.Errorf("aturan budget %q tidak valid (format pola=token, mis. *mini*=800)", part)
		}
		b.Rules = append(b.Rules, Rule{Pattern: pattern, Tokens: tokens})
	}
	return b, nil
}

// For mengembalikan budget untuk sebuah model: aturan pertama yang cocok,
// atau Default bila tidak ada.
func (b Budgets) For(model string) int {
	m := strings.ToLower(model)
	for _, r := range b.Rules {
		if wildcard(r.Pattern, m) {
			return r.Tokens
		}
	}
	return b.Default
}

// wildcard mencocokkan s dengan pola yang memakai "*" sebagai wildcard.
func wildcard(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0

	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}

	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
