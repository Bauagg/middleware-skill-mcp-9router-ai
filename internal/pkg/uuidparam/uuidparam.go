// Package uuidparam memvalidasi parameter path berformat UUID, supaya id yang
// salah ditolak 400 di depan dan tidak sampai ke PostgreSQL sebagai error 500.
package uuidparam

import (
	"regexp"

	"github.com/gin-gonic/gin"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Valid melaporkan apakah s berformat UUID.
func Valid(s string) bool {
	return uuidRe.MatchString(s)
}

// Require adalah middleware Gin yang menolak request bila parameter path
// name bukan UUID.
func Require(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !Valid(c.Param(name)) {
			apperror.Handle(c, apperror.NewBadRequest("Validasi gagal",
				response.FieldError{Field: name, Message: name + " harus UUID"}))
			c.Abort()
			return
		}
		c.Next()
	}
}
