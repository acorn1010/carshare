// Package logging sets up structured JSON logs on stdout so the log pipeline
// (Loki, CloudWatch, anything) can parse fields without regexes.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON slog.Logger at the given level. Unknown levels fall back
// to info instead of failing startup.
func New(level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(level)}))
}

// SetDefault installs the logger as the process-wide default.
func SetDefault(logger *slog.Logger) {
	slog.SetDefault(logger)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
