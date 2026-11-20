package middleware

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
)

// Recovery menangkap panic agar server tidak crash, mencatatnya, lalu
// membalas 500 dengan amplop standar.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		logger.Error("panic recovered",
			zap.Any("error", recovered),
			zap.String("path", c.Request.URL.Path),
			zap.String("method", c.Request.Method),
			zap.Stack("stack"),
		)
		response.ServerError(c, "Terjadi kesalahan pada server")
	})
}
