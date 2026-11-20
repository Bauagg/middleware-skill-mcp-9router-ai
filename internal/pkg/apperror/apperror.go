// Package apperror menyediakan tipe error aplikasi + pemetaan terpusat ke
// respons HTTP. Ini padanan Go dari `src/utils/app-error.ts` (AppError +
// handleError) di template Express.
package apperror

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

// AppError adalah error yang membawa status HTTP + detail field.
type AppError struct {
	Message    string
	StatusCode int
	Status     string // fail | error
	Errors     []response.FieldError
}

func (e *AppError) Error() string { return e.Message }

func newAppError(message string, code int, errs []response.FieldError) *AppError {
	status := "fail"
	if code >= http.StatusInternalServerError {
		status = "error"
	}
	return &AppError{Message: message, StatusCode: code, Status: status, Errors: errs}
}

func orDefault(m, def string) string {
	if strings.TrimSpace(m) == "" {
		return def
	}
	return m
}

// NewBadRequest membuat error 400.
func NewBadRequest(message string, errs ...response.FieldError) *AppError {
	return newAppError(orDefault(message, "Request tidak valid"), http.StatusBadRequest, errs)
}

// NewUnauthorized membuat error 401.
func NewUnauthorized(message string, errs ...response.FieldError) *AppError {
	return newAppError(orDefault(message, "Tidak terautentikasi"), http.StatusUnauthorized, errs)
}

// NewForbidden membuat error 403.
func NewForbidden(message string, errs ...response.FieldError) *AppError {
	return newAppError(orDefault(message, "Tidak memiliki akses"), http.StatusForbidden, errs)
}

// NewNotFound membuat error 404.
func NewNotFound(message string, errs ...response.FieldError) *AppError {
	return newAppError(orDefault(message, "Data tidak ditemukan"), http.StatusNotFound, errs)
}

// NewConflict membuat error 409.
func NewConflict(message string, errs ...response.FieldError) *AppError {
	return newAppError(orDefault(message, "Data sudah ada"), http.StatusConflict, errs)
}

// NewInternalServerError membuat error 500.
func NewInternalServerError(message string, errs ...response.FieldError) *AppError {
	return newAppError(orDefault(message, "Terjadi kesalahan pada server"), http.StatusInternalServerError, errs)
}

// Handle memetakan error apa pun ke amplop respons standar.
func Handle(c *gin.Context, err error) {
	if err == nil {
		return
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		c.JSON(appErr.StatusCode, response.Envelope{
			Status:  appErr.Status,
			Message: appErr.Message,
			Errors:  appErr.Errors,
		})
		return
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "Data tidak ditemukan")
		return
	}
	if isUniqueViolation(err) {
		response.Conflict(c, "Data sudah digunakan")
		return
	}

	response.ServerError(c, "")
}

// isUniqueViolation mendeteksi error unique constraint PostgreSQL (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLSTATE 23505") || strings.Contains(s, "duplicate key value")
}

// BindingErrors mengubah error validasi gin/validator menjadi daftar FieldError.
func BindingErrors(err error) []response.FieldError {
	var ves validator.ValidationErrors
	if errors.As(err, &ves) {
		out := make([]response.FieldError, 0, len(ves))
		for _, fe := range ves {
			out = append(out, response.FieldError{
				Field:   toSnake(fe.Field()),
				Message: messageFor(fe),
			})
		}
		return out
	}
	return []response.FieldError{{Field: "body", Message: "Format request tidak valid"}}
}

func messageFor(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "wajib diisi"
	case "email":
		return "format email tidak valid"
	case "min":
		return "minimal " + fe.Param() + " karakter"
	case "max":
		return "maksimal " + fe.Param() + " karakter"
	case "oneof":
		return "harus salah satu dari: " + fe.Param()
	default:
		return "tidak valid (" + fe.Tag() + ")"
	}
}

// toSnake mengubah NamaField menjadi nama_field agar cocok dengan json tag.
func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
