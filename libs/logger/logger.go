package logger

import (
	"log/slog"
	"os"
)

// New creates a JSON logger bound to a specific service name.
func New(service string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{})
	return slog.New(handler).With("service", service)
}

