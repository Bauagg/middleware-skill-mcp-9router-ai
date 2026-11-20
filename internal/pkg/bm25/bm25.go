// Package bm25 berisi mesin pencarian BM25 sederhana tanpa dependensi luar:
// tokenizer (Indonesia + Inggris) dan penghitung skor. Tidak tahu apa pun soal
// skill; pemanggil yang menentukan isi dokumen.
package bm25

import "math"

// Parameter BM25 standar.
const (
	k1 = 1.5
	b  = 0.75
)

// Corpus adalah statistik sekumpulan dokumen. Dibangun sekali, lalu hanya
// dibaca, jadi aman dipakai banyak goroutine.
type Corpus struct {
	docs   []map[string]int // frekuensi term per dokumen
	lens   []int
	df     map[string]int // jumlah dokumen yang memuat term
	avgLen float64
}

// New membangun Corpus dari token tiap dokumen (hasil Tokenize, boleh diberi
// bobot dengan mengulang token).
func New(docs [][]string) *Corpus {
	c := &Corpus{
		docs: make([]map[string]int, len(docs)),
		lens: make([]int, len(docs)),
		df:   map[string]int{},
	}

	total := 0
	for i, tokens := range docs {
		tf := map[string]int{}
		for _, t := range tokens {
			tf[t]++
		}
		for t := range tf {
			c.df[t]++
		}
		c.docs[i], c.lens[i] = tf, len(tokens)
		total += len(tokens)
	}
	if len(docs) > 0 {
		c.avgLen = float64(total) / float64(len(docs))
	}
	return c
}

// Score menghitung skor BM25 tiap dokumen untuk query (token hasil Tokenize).
// Hasilnya sejajar dengan urutan dokumen saat New. Memakai idf varian Lucene
// yang tidak pernah negatif.
func (c *Corpus) Score(query []string) []float64 {
	scores := make([]float64, len(c.docs))
	if c.avgLen == 0 {
		return scores
	}

	n := float64(len(c.docs))
	seen := map[string]bool{}
	for _, term := range query {
		if seen[term] {
			continue
		}
		seen[term] = true

		df := float64(c.df[term])
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))

		for i, tf := range c.docs {
			f := float64(tf[term])
			if f == 0 {
				continue
			}
			norm := k1 * (1 - b + b*float64(c.lens[i])/c.avgLen)
			scores[i] += idf * f * (k1 + 1) / (f + norm)
		}
	}
	return scores
}
