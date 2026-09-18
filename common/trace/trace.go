// Package trace provides lightweight distributed tracing via context-propagated
// trace IDs and span tracking. It is dependency-free and designed to be
// upgradeable to OpenTelemetry if full OTLP export is needed later.
//
// Usage:
//
//	// In an HTTP middleware, start a trace from the request:
//	ctx = trace.WithTraceID(r.Context(), trace.NewTraceID())
//
//	// In a handler or engine pipeline, start a span:
//	ctx, span := trace.Start(ctx, "modbus.Read")
//	defer span.End() // logs duration and records the span
//
//	// The trace ID is available via:
//	tid := trace.TraceIDFromContext(ctx)
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// traceIDKey is the context key for trace IDs.
type traceIDKey struct{}

// spanKey is the context key for the current span name.
type spanKey struct{}

// NewTraceID generates a 16-byte hex-encoded trace ID (32 chars),
// matching W3C Trace Context format.
func NewTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails
		return hex.EncodeToString([]byte(time.Now().Format("200601021504050000")))
	}
	return hex.EncodeToString(b)
}

// WithTraceID returns a context with the given trace ID.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceIDFromContext extracts the trace ID from the context.
// Returns empty string if no trace ID is set.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, ok := ctx.Value(traceIDKey{}).(string)
	if !ok {
		return ""
	}
	return v
}

// Span represents a named, timed operation within a trace.
type Span struct {
	traceID   string
	name      string
	startTime time.Time
	attrs     []slog.Attr
	mu        sync.Mutex
	ended     atomic.Bool
}

// noopSpan is a shared, pre-ended Span returned by Start when debug-level
// logging is disabled. Because it is already ended, End and SetAttr are
// no-ops, so the hot readFromDriver path avoids allocating a *Span and the
// spanKey valueCtx on every driver read.
var noopSpan = func() *Span {
	s := &Span{}
	s.ended.Store(true)
	return s
}()

// Start creates a new span within the trace identified by the context.
// If no trace ID is in the context, a new one is generated.
// The returned context carries both the trace ID and span name.
//
// When slog's debug level is disabled (the default), Start short-circuits: it
// returns the original context unchanged together with the shared no-op Span,
// avoiding the *Span and spanKey valueCtx allocations. The trace ID is only
// consumed by the debug log emitted on End, so propagating it when debug
// logging is off would be wasted work on the readFromDriver hot path.
func Start(ctx context.Context, name string) (context.Context, *Span) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return ctx, noopSpan
	}
	traceID := TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = NewTraceID()
		ctx = WithTraceID(ctx, traceID)
	}
	ctx = context.WithValue(ctx, spanKey{}, name)
	span := &Span{
		traceID:   traceID,
		name:      name,
		startTime: time.Now(),
	}
	return ctx, span
}

// SetAttr adds a key-value attribute to the span.
// It is a no-op on an ended span, including the shared no-op Span returned by
// Start when debug logging is disabled.
func (s *Span) SetAttr(key string, value any) {
	if s.ended.Load() {
		return
	}
	s.mu.Lock()
	s.attrs = append(s.attrs, slog.Any(key, value))
	s.mu.Unlock()
}

// End records the span duration and logs it at debug level.
// Calling End more than once is a no-op.
func (s *Span) End() {
	if !s.ended.CompareAndSwap(false, true) {
		return
	}
	// Defense-in-depth: Start() already short-circuits when debug logging is
	// disabled, but a Span created via another path (or a default logger
	// reconfigured after Start) should still avoid building the args slice.
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	duration := time.Since(s.startTime)
	args := []any{
		"trace_id", s.traceID,
		"span", s.name,
		"duration", duration,
	}
	s.mu.Lock()
	for _, a := range s.attrs {
		args = append(args, a.Key, a.Value.Any())
	}
	s.mu.Unlock()
	slog.Debug("trace span", args...)
}

// SpanNameFromContext extracts the current span name from the context.
func SpanNameFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, ok := ctx.Value(spanKey{}).(string)
	if !ok {
		return ""
	}
	return v
}
