// Package logger provides structured logging to stdout only (no file sinks).
package logger

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// Op is the logical operation name (e.g. seal_enqueue, batch_process).
const Op = "op"

// RequestID correlates HTTP requests.
const RequestID = "request_id"

// BatchID correlates batch jobs.
const BatchID = "batch_id"

// NewStdout returns a slog.Logger that writes only to os.Stdout (no file handlers).
func NewStdout(level slog.Level) *slog.Logger {
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level:     level,
		AddSource: level <= slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{
					Key:   slog.TimeKey,
					Value: slog.StringValue(a.Value.Time().UTC().Format(time.RFC3339Nano)),
				}
			}
			return a
		},
	})
	return slog.New(h)
}

// LevelFromEnv reads LOG_LEVEL (debug, info, warn, error); default info.
func LevelFromEnv() slog.Level {
	return ParseLevel(os.Getenv("LOG_LEVEL"))
}

// ParseLevel maps a string to slog.Level.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info", "":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}
