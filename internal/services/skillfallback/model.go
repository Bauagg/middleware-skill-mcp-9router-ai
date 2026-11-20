package skillfallback

// Candidate adalah satu skill yang ditawarkan ke model untuk dipilih.
type Candidate struct {
	Dir         string
	Name        string
	Description string
}

// Result adalah keputusan pemilih cadangan.
type Result struct {
	// Dir adalah folder skill terpilih; "" berarti model memutuskan tidak ada
	// skill yang cocok.
	Dir string

	// Tokens adalah token yang dipakai panggilan ini (dari laporan provider,
	// atau perkiraan bila tidak dilaporkan).
	Tokens int
}
