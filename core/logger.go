package core

// Logger is the port for structured logging from drivers and other
// components.
//
// Injection: drivers implementing SetLogger receive the project's
// log.DefaultLogger from the engine's driver manager at Init time;
// drivers that don't implement the injector keep their internal default.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// NoopLogger is a Logger implementation that discards every call. It is
// the zero-dependency default for drivers and components that do not log,
// so they can hold a Logger value without a real backend.
type NoopLogger struct{}

func (NoopLogger) Debug(msg string, args ...any) {}

func (NoopLogger) Info(msg string, args ...any) {}

func (NoopLogger) Warn(msg string, args ...any) {}

func (NoopLogger) Error(msg string, args ...any) {}

var _ Logger = NoopLogger{}
