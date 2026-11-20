// Package response menyediakan amplop respons JSON seragam.
// Ini padanan Go dari `src/utils/api-response.ts` di template Express:
// { status: success|fail|error, message, data?, errors? }.
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// FieldError adalah detail error per-field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Envelope adalah bentuk respons standar semua endpoint.
type Envelope struct {
	Status  string       `json:"status"`
	Message string       `json:"message"`
	Data    interface{}  `json:"data,omitempty"`
	Errors  []FieldError `json:"errors,omitempty"`
}

// Success mengirim 200 dengan data.
func Success(c *gin.Context, data interface{}, message string) {
	if message == "" {
		message = "Berhasil"
	}
	c.JSON(http.StatusOK, Envelope{Status: "success", Message: message, Data: data})
}

// Created mengirim 201 dengan data.
func Created(c *gin.Context, data interface{}, message string) {
	if message == "" {
		message = "Data berhasil dibuat"
	}
	c.JSON(http.StatusCreated, Envelope{Status: "success", Message: message, Data: data})
}

// Accepted mengirim 202: permintaan diterima dan diproses di latar belakang.
func Accepted(c *gin.Context, data interface{}, message string) {
	if message == "" {
		message = "Permintaan diterima dan sedang diproses"
	}
	c.JSON(http.StatusAccepted, Envelope{Status: "success", Message: message, Data: data})
}

// BadRequest mengirim 400.
func BadRequest(c *gin.Context, message string, errs []FieldError) {
	if message == "" {
		message = "Request tidak valid"
	}
	c.JSON(http.StatusBadRequest, Envelope{Status: "fail", Message: message, Errors: errs})
}

// Unauthorized mengirim 401.
func Unauthorized(c *gin.Context, message string, errs []FieldError) {
	if message == "" {
		message = "Tidak terautentikasi"
	}
	c.JSON(http.StatusUnauthorized, Envelope{Status: "fail", Message: message, Errors: errs})
}

// Forbidden mengirim 403.
func Forbidden(c *gin.Context, message string) {
	if message == "" {
		message = "Tidak memiliki akses"
	}
	c.JSON(http.StatusForbidden, Envelope{Status: "fail", Message: message})
}

// NotFound mengirim 404.
func NotFound(c *gin.Context, message string) {
	if message == "" {
		message = "Data tidak ditemukan"
	}
	c.JSON(http.StatusNotFound, Envelope{Status: "fail", Message: message})
}

// Conflict mengirim 409.
func Conflict(c *gin.Context, message string) {
	if message == "" {
		message = "Data sudah ada"
	}
	c.JSON(http.StatusConflict, Envelope{Status: "fail", Message: message})
}

// ServerError mengirim 500.
func ServerError(c *gin.Context, message string) {
	if message == "" {
		message = "Terjadi kesalahan pada server"
	}
	c.JSON(http.StatusInternalServerError, Envelope{Status: "error", Message: message})
}

// Meta berisi informasi paginasi.
type Meta struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

// Paginated mengirim 200 dengan data + meta paginasi.
func Paginated(c *gin.Context, data interface{}, meta Meta, message string) {
	if message == "" {
		message = "Berhasil mengambil data"
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": message,
		"data":    data,
		"meta":    meta,
	})
}
