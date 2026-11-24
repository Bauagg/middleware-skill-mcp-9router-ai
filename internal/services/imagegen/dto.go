package imagegen

// toolArgs adalah argumen tool generate_image dari model.
type toolArgs struct {
	Prompt string `json:"prompt"`
	Alt    string `json:"alt"`
}

// ListFilter adalah parameter GET /api/images.
type ListFilter struct {
	UserID string
	Query  string // cari di prompt dan alt (kosong = semua)
	Page   int
	Limit  int
}
