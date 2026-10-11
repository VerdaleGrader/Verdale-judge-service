// Package response writes the contract's ErrorResponse, the one error shape
// shared by every Verdale service.
package response

import (
	"errors"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	apperrors "github.com/kanonkc/verdale-judge-service/errors"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

const (
	HeaderRequestID  = "X-Request-Id"
	ContextRequestID = "request_id"
	ContextLogger    = "logger"
)

// Error writes err as an ErrorResponse. Unknown errors become a generic 500
// so internal details never reach the caller; they are logged instead.
func Error(ctx *gin.Context, err error) {
	var appErr *apperrors.AppError
	if !errors.As(err, &appErr) {
		Logger(ctx).Error("unhandled error", zap.Error(err))
		appErr = apperrors.Internal()
	}
	body := contract.ErrorResponse{
		Status:    appErr.Status,
		ErrorCode: appErr.Code,
		Message:   appErr.Message,
		RequestId: ctx.GetString(ContextRequestID),
	}
	if len(appErr.Details) > 0 {
		details := appErr.Details
		body.Details = &details
	}
	ctx.AbortWithStatusJSON(appErr.Status, body)
}

// Logger returns the request-scoped logger set by the request-id middleware.
func Logger(ctx *gin.Context) *zap.Logger {
	if value, ok := ctx.Get(ContextLogger); ok {
		if requestLogger, ok := value.(*zap.Logger); ok {
			return requestLogger
		}
	}
	return zap.L()
}
