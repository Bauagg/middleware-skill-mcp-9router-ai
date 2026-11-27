package aiaccount

// CreateProviderInput adalah body POST /api/ai/providers (admin).
type CreateProviderInput struct {
	Driver     string `json:"driver" binding:"required,max=30"`
	Name       string `json:"name" binding:"required,max=100"`
	LoginURL   string `json:"login_url" binding:"omitempty,url,max=2000"`
	BaseURL    string `json:"base_url" binding:"omitempty,url,max=2000"`
	ChatModel  string `json:"chat_model" binding:"max=200"`
	ImageModel string `json:"image_model" binding:"max=200"`
}

// UpdateProviderInput adalah body PATCH /api/ai/providers/:id (admin). Field
// yang tidak dikirim tidak diubah; LoginURL "" kembali ke halaman login bawaan.
type UpdateProviderInput struct {
	Name       *string `json:"name" binding:"omitempty,max=100"`
	Enabled    *bool   `json:"enabled"`
	Priority   *int    `json:"priority"`
	LoginURL   *string `json:"login_url" binding:"omitempty,max=2000"`
	BaseURL    *string `json:"base_url" binding:"omitempty,max=2000"`
	ChatModel  *string `json:"chat_model" binding:"omitempty,max=200"`
	ImageModel *string `json:"image_model" binding:"omitempty,max=200"`
}

// CreateAccountInput adalah body POST /api/ai/accounts.
type CreateAccountInput struct {
	ProviderID string `json:"provider_id" binding:"required,uuid"`
	Name       string `json:"name" binding:"required,max=100"`
	// Cookies (provider login browser): header Cookie dari DevTools, ekspor
	// JSON ekstensi cookie, atau isi cookies.txt dari situsnya saat sudah login.
	Cookies string `json:"cookies"`
	// APIKey (provider API key).
	APIKey string `json:"api_key" binding:"max=1000"`
}

// UpdateAccountInput adalah body PATCH /api/ai/accounts/:id. Field yang
// tidak dikirim tidak diubah.
type UpdateAccountInput struct {
	Name     *string `json:"name" binding:"omitempty,max=100"`
	Cookies  *string `json:"cookies"`
	APIKey   *string `json:"api_key" binding:"omitempty,max=1000"`
	Enabled  *bool   `json:"enabled"`
	Priority *int    `json:"priority"`
}

// VideoFilter adalah filter daftar video.
type VideoFilter struct {
	UserID string // "" = semua user (admin)
	Query  string
	Page   int
	Limit  int
}
