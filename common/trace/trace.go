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

// Start creates a new span within the trace identified by the context.
// If no trace ID is in the context, a new one is generated.
// The returned context carries both the trace ID and span name.
func Start(ctx context.Context, name string) (context.Context, *Span) {
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
func (s *Span) SetAttr(key string, value any) {
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

// ContextWithTrace returns a context that has a trace ID, generating
// one if the parent context doesn't already have one. This is useful
// at pipeline entry points (e.g., when a driver reads data).
func ContextWithTrace(ctx context.Context) (outCtx context.Context, traceID string) {
	traceID = TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = NewTraceID()
		ctx = WithTraceID(ctx, traceID)
	}
	return ctx, traceID
}
