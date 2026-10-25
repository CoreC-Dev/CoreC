package core

// Logger is the port for structured logging from drivers and other
// components. Defining it as an interface in the core port package keeps
// concrete logging backends (slog, the project's log package, a test
// capture, …) out of the domain and lets each component depend on exactly
// this capability.
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

// Debug satisfies Logger; it does nothing.
func (NoopLogger) Debug(msg string, args ...any) {}

// Info satisfies Logger; it does nothing.
func (NoopLogger) Info(msg string, args ...any) {}

// Warn satisfies Logger; it does nothing.
func (NoopLogger) Warn(msg string, args ...any) {}

// Error satisfies Logger; it does nothing.
func (NoopLogger) Error(msg string, args ...any) {}

// Compile-time assertion that NoopLogger satisfies Logger.
var _ Logger = NoopLogger{}
