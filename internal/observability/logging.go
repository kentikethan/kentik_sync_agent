// Package observability provides the agent's structured logging and its
// Kentik NMS metrics push (see metrics.go). There's no scraped /metrics
// endpoint or health-check HTTP server — MVP scope keeps the agent to a
// single process with no extra listeners; metrics go out, nothing comes in.
package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger builds the process-wide structured logger. format is "json"
// (default, suited to log aggregators) or "text" (more readable for local
// development). level is any slog level name ("debug", "info", "warn",
// "error"), defaulting to "info".
func NewLogger(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	if strings.EqualFold(format, "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
