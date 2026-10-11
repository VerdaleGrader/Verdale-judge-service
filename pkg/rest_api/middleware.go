// Package rest_api holds the gin middleware shared by every route.
package rest_api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	apperrors "github.com/kanonkc/verdale-judge-service/errors"
	"github.com/kanonkc/verdale-judge-service/pkg/hmacauth"
	"github.com/kanonkc/verdale-judge-service/pkg/logger"
	"github.com/kanonkc/verdale-judge-service/pkg/response"
)

// ContextRawBody holds the request body that was verified by the HMAC
// middleware, so handlers decode exactly the bytes that were signed.
const ContextRawBody = "raw_body"

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// RequestID reuses a well-formed X-Request-Id from the caller (Core) or
// creates one, echoes it back and attaches a request-scoped logger.
func RequestID(base *zap.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		requestID := ctx.GetHeader(response.HeaderRequestID)
		if !requestIDPattern.MatchString(requestID) {
			requestID = uuid.NewString()
		}
		ctx.Set(response.ContextRequestID, requestID)
		ctx.Set(response.ContextLogger, base.With(zap.String(logger.FieldRequestID, requestID)))
		ctx.Header(response.HeaderRequestID, requestID)
		ctx.Next()
	}
}

// AccessLog writes one line per request.
func AccessLog() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		started := time.Now()
		ctx.Next()
		if ctx.Request.URL.Path == "/health" || ctx.Request.URL.Path == "/metrics" {
			return
		}
		response.Logger(ctx).Info("request",
			zap.String("method", ctx.Request.Method),
			zap.String("path", ctx.Request.URL.Path),
			zap.Int("status", ctx.Writer.Status()),
			zap.Duration("took", time.Since(started)),
		)
	}
}

// Recovery turns a panic into the standard 500 error.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(ctx *gin.Context, recovered any) {
		response.Logger(ctx).Error("panic while handling request", zap.Any("panic", recovered), zap.Stack("stack"))
		response.Error(ctx, apperrors.Internal())
	})
}

// VerifySignature reads the body (up to maxBodyBytes), verifies the HMAC
// signature against those exact bytes and makes them available to the
// handler. Requests that fail never reach a handler.
func VerifySignature(verifier hmacauth.Verifier, maxBodyBytes int64) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		body, err := io.ReadAll(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				response.Error(ctx, apperrors.New(http.StatusRequestEntityTooLarge, apperrors.CodeBodyTooLarge, "The request body is too large."))
				return
			}
			response.Error(ctx, apperrors.New(http.StatusBadRequest, apperrors.CodeInvalidJSON, "The request body could not be read."))
			return
		}
		keyID, err := verifier.Verify(ctx.Request.Header, ctx.Request.Method, ctx.Request.URL.RequestURI(), body)
		if err != nil {
			response.Logger(ctx).Warn("rejected unsigned or badly signed request", zap.String("key_id", keyID), zap.Error(err))
			response.Error(ctx, signatureError(err))
			return
		}
		ctx.Set(ContextRawBody, body)
		ctx.Request.Body = io.NopCloser(bytes.NewReader(body))
		ctx.Next()
	}
}

func signatureError(err error) *apperrors.AppError {
	switch {
	case errors.Is(err, hmacauth.ErrUnknownKeyID):
		return apperrors.Unauthorized(apperrors.CodeUnknownKeyID, "The signing key is not recognised.")
	case errors.Is(err, hmacauth.ErrStaleTimestamp):
		return apperrors.Unauthorized(apperrors.CodeStaleTimestamp, "The request timestamp is outside the accepted window.")
	case errors.Is(err, hmacauth.ErrInvalidSignature):
		return apperrors.Unauthorized(apperrors.CodeInvalidSignature, "The request signature is invalid.")
	default:
		return apperrors.Unauthorized(apperrors.CodeMissingSignature, "The request must be signed.")
	}
}
