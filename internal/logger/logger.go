package logger

import (
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a new logger based on the log level.
// level: "debug", "info", "warn", "error"
func New(level string) (*zap.Logger, error) {
	var cfg zap.Config

	switch strings.ToLower(level) {
	case "debug":
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	default:
		cfg = zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(parseLevel(level))
	}

	logger, err := cfg.Build()
	if err != nil {
		return nil, err
	}
	return logger, nil
}

func parseLevel(level string) zapcore.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
