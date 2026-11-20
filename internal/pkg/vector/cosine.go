// Package vector berisi perhitungan vektor untuk pencarian berbasis embedding.
package vector

import "math"

// Cosine menghitung kemiripan kosinus dua vektor: 1 = arah sama persis,
// 0 = tidak berhubungan, -1 = berlawanan. Mengembalikan 0 bila panjang
// vektor berbeda atau salah satunya nol.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}

	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
