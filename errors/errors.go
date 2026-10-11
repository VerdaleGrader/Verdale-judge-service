// Package errors defines the typed errors Judge returns over HTTP. Every
// one renders as the contract's ErrorResponse via pkg/response.
package errors

import (
	"fmt"
	"net/http"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
)

// Error codes are stable strings that Core may branch on.
const (
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeInvalidJSON      = "INVALID_JSON"
	CodeBodyTooLarge     = "BODY_TOO_LARGE"
	CodeJobConflict      = "JOB_ID_CONFLICT"
	CodeJobNotFound      = "JOB_NOT_FOUND"
	CodeNotFound         = "NOT_FOUND"
	CodeQueueUnavailable = "QUEUE_UNAVAILABLE"
	CodeInternal         = "INTERNAL_ERROR"
	CodeMissingSignature = "MISSING_SIGNATURE"
	CodeUnknownKeyID     = "UNKNOWN_KEY_ID"
	CodeStaleTimestamp   = "STALE_TIMESTAMP"
	CodeInvalidSignature = "INVALID_SIGNATURE"
	CodeUnsupportedMedia = "UNSUPPORTED_MEDIA_TYPE"
)

// AppError is an error with an HTTP status and a stable code.
type AppError struct {
	Status  int
	Code    string
	Message string
	Details []contract.ErrorDetail
}

func (e *AppError) Error() string {
	return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Message)
}

func New(status int, code, message string) *AppError {
	return &AppError{Status: status, Code: code, Message: message}
}

func Validation(details []contract.ErrorDetail) *AppError {
	return &AppError{Status: http.StatusBadRequest, Code: CodeValidationFailed, Message: "The job request is invalid.", Details: details}
}

func Unauthorized(code, message string) *AppError {
	return New(http.StatusUnauthorized, code, message)
}

func Internal() *AppError {
	return New(http.StatusInternalServerError, CodeInternal, "Internal server error.")
}
