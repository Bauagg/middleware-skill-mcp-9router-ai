package contextpack

// CreateInput adalah body POST /api/context-packs.
type CreateInput struct {
	Name    string `json:"name" binding:"required,max=100"`
	Content string `json:"content" binding:"required,max=20000"`
	// Active kosong = true.
	Active *bool `json:"active"`
}

// UpdateInput adalah body PUT /api/context-packs/:id; field kosong tidak diubah.
type UpdateInput struct {
	Name    *string `json:"name" binding:"omitempty,min=1,max=100"`
	Content *string `json:"content" binding:"omitempty,min=1,max=20000"`
	Active  *bool   `json:"active"`
}
