package log

import (
	"log/slog"

	"github.com/CoreC-Dev/CoreC/core"
)

// SlogLogger adapts the standard log/slog package to the core.Logger
// port. It lets drivers and other components depend on core.Logger
// (an interface) while the actual logging goes through slog — the
// project's concrete backend. In tests, a NoopLogger or custom
// capture can be injected instead, decoupling the component from slog.
type SlogLogger struct{}

// Debug logs at the slog.Debug level.
func (SlogLogger) Debug(msg string, args ...any) { slog.Debug(msg, args...) }

// Info logs at the slog.Info level.
func (SlogLogger) Info(msg string, args ...any) { slog.Info(msg, args...) }

// Warn logs at the slog.Warn level.
func (SlogLogger) Warn(msg string, args ...any) { slog.Warn(msg, args...) }

// Error logs at the slog.Error level.
func (SlogLogger) Error(msg string, args ...any) { slog.Error(msg, args...) }

// Compile-time assertion that SlogLogger satisfies core.Logger.
var _ core.Logger = SlogLogger{}

// DefaultLogger returns the standard slog-backed Logger. Components
// that don't receive an explicit Logger should use this as their
// default instead of calling slog directly, so they can be tested
// with a mock Logger without touching the global slog state.
func DefaultLogger() core.Logger {
	return SlogLogger{}
}
