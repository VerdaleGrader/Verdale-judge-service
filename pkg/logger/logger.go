// Package logger builds the process-wide structured logger. Every log line
// about a job carries a "job_id" field; every line about an HTTP request
// carries "request_id".
package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	FieldJobID     = "job_id"
	FieldRequestID = "request_id"
)

// New returns a JSON logger for production or a console logger for local
// development. Call Sync on shutdown so buffered lines are flushed.
func New(level, format, environment, version string) (*zap.Logger, error) {
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log level %q: %w", level, err)
	}

	var config zap.Config
	switch format {
	case "json":
		config = zap.NewProductionConfig()
		config.EncoderConfig.TimeKey = "timestamp"
		config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	case "console":
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	default:
		return nil, fmt.Errorf("log format %q must be json or console", format)
	}
	config.Level = zap.NewAtomicLevelAt(zapLevel)
	config.DisableStacktrace = true

	built, err := config.Build()
	if err != nil {
		return nil, err
	}
	return built.With(zap.String("service", "verdale-judge"), zap.String("environment", environment), zap.String("version", version)), nil
}

// ForJob returns a child logger that tags every line with the job id.
func ForJob(base *zap.Logger, jobID string) *zap.Logger {
	return base.With(zap.String(FieldJobID, jobID))
}
