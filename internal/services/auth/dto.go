package auth

import "github.com/Bauagg/middleware-skill-ai/internal/pkg/jwtauth"

// RegisterInput adalah body POST /api/auth/register.
// Password maksimal 72 byte karena bcrypt mengabaikan sisanya.
type RegisterInput struct {
	Email    string `json:"email" binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

// LoginInput adalah body POST /api/auth/login.
type LoginInput struct {
	Email    string `json:"email" binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,max=72"`
}

// RefreshInput adalah body POST /api/auth/refresh.
type RefreshInput struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// AuthResult adalah respons register, login, dan refresh.
type AuthResult struct {
	User *User `json:"user"`
	*jwtauth.Pair
}
