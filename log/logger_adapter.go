package log

import (
	"log/slog"

	"github.com/CoreC-Dev/CoreC/core"
)

// SlogLogger adapts the standard log/slog package to the core.Logger
// port.
type SlogLogger struct{}

func (SlogLogger) Debug(msg string, args ...any) { slog.Debug(msg, args...) }

func (SlogLogger) Info(msg string, args ...any) { slog.Info(msg, args...) }

func (SlogLogger) Warn(msg string, args ...any) { slog.Warn(msg, args...) }

func (SlogLogger) Error(msg string, args ...any) { slog.Error(msg, args...) }

// Compile-time assertion that SlogLogger satisfies core.Logger.
var _ core.Logger = SlogLogger{}

// DefaultLogger returns the standard slog-backed Logger.
func DefaultLogger() core.Logger {
	return SlogLogger{}
}
