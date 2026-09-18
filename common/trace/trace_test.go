package trace

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withDebugLogger replaces slog's default logger with one that enables
// debug-level records, restoring the previous logger when the test ends.
// Start only allocates a real Span when debug logging is enabled, so tests
// that exercise Span fields must opt in.
func withDebugLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// withInfoLogger replaces slog's default logger with one that disables
// debug-level records (the project default), restoring the previous logger
// when the test ends.
func withInfoLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

func TestNewTraceID(t *testing.T) {
	id1 := NewTraceID()
	id2 := NewTraceID()
	if id1 == id2 {
		t.Error("trace IDs should be unique")
	}
	if len(id1) != 32 {
		t.Errorf("trace ID should be 32 hex chars, got %d", len(id1))
	}
}

func TestTraceIDContext(t *testing.T) {
	ctx := context.Background()
	if TraceIDFromContext(ctx) != "" {
		t.Error("empty context should have empty trace ID")
	}

	traceID := "abc123def456"
	ctx = WithTraceID(ctx, traceID)
	if got := TraceIDFromContext(ctx); got != traceID {
		t.Errorf("got %q, want %q", got, traceID)
	}
}

func TestSpanStartEnd(t *testing.T) {
	withDebugLogger(t)
	ctx := context.Background()
	ctx, span := Start(ctx, "test.operation")
	if span == nil {
		t.Fatal("span should not be nil")
	}
	if span == noopSpan {
		t.Fatal("Start should allocate a real span when debug logging is enabled")
	}
	if TraceIDFromContext(ctx) == "" {
		t.Error("context should have a trace ID after Start")
	}
	if SpanNameFromContext(ctx) != "test.operation" {
		t.Error("context should have the span name")
	}
	span.SetAttr("key1", "value1")
	span.SetAttr("key2", 42)
	span.End()
	// Double end should be a no-op
	span.End()
}

func TestSpanWithExistingTraceID(t *testing.T) {
	withDebugLogger(t)
	ctx := context.Background()
	traceID := "existing-trace-id"
	ctx = WithTraceID(ctx, traceID)
	_, span := Start(ctx, "child.operation")
	if span.traceID != traceID {
		t.Errorf("span should inherit trace ID %q, got %q", traceID, span.traceID)
	}
	span.End()
}

func TestSpanDuration(t *testing.T) {
	withDebugLogger(t)
	ctx := context.Background()
	_, span := Start(ctx, "timed.operation")
	if span == noopSpan {
		t.Fatal("Start should allocate a real span when debug logging is enabled")
	}
	time.Sleep(2 * time.Millisecond)
	span.End()
	if !span.ended.Load() {
		t.Error("span should be marked as ended")
	}
}

// TestStartNoopWhenDebugDisabled verifies that when debug logging is disabled
// (the project default), Start returns the shared no-op Span without modifying
// the context, so the hot readFromDriver path pays no allocation cost.
func TestStartNoopWhenDebugDisabled(t *testing.T) {
	withInfoLogger(t)
	ctx := context.Background()
	out, span := Start(ctx, "noop.operation")
	if span != noopSpan {
		t.Fatal("expected shared noopSpan when debug logging is disabled")
	}
	if out != ctx {
		t.Error("context should be unchanged when debug logging is disabled")
	}
	if TraceIDFromContext(out) != "" {
		t.Error("no trace ID should be propagated when debug logging is disabled")
	}
	if SpanNameFromContext(out) != "" {
		t.Error("no span name should be propagated when debug logging is disabled")
	}
	// SetAttr and End must be safe no-ops on the shared pre-ended span.
	span.SetAttr("key", "value")
	span.End()
	span.End()
	if !span.ended.Load() {
		t.Error("noopSpan should remain ended")
	}
}

// TestSetAttrNoopOnEndedSpan verifies SetAttr is a no-op once a span has ended.
func TestSetAttrNoopOnEndedSpan(t *testing.T) {
	withDebugLogger(t)
	ctx := context.Background()
	_, span := Start(ctx, "ended.operation")
	span.End()
	// After End, SetAttr must not append (span is ended).
	span.SetAttr("late", "attr")
	span.mu.Lock()
	if len(span.attrs) != 0 {
		t.Errorf("expected no attrs after End, got %d", len(span.attrs))
	}
	span.mu.Unlock()
}

func TestParseTraceparent(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "valid traceparent",
			input:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7160204fbd-01",
			expect: "0af7651916cd43dd8448eb211c80319c",
		},
		{
			name:   "wrong version",
			input:  "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7160204fbd-01",
			expect: "",
		},
		{
			name:   "too few parts",
			input:  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7160204fbd",
			expect: "",
		},
		{
			name:   "trace id too short",
			input:  "00-deadbeef-b7ad6b7160204fbd-01",
			expect: "",
		},
		{
			name:   "all-zero trace id",
			input:  "00-00000000000000000000000000000000-b7ad6b7160204fbd-01",
			expect: "",
		},
		{
			name:   "non-hex trace id",
			input:  "00-0af7651916cd43dd8448eb211c80319z-b7ad6b7160204fbd-01",
			expect: "",
		},
		{
			name:   "empty input",
			input:  "",
			expect: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTraceparent(tc.input)
			if got != tc.expect {
				t.Errorf("parseTraceparent(%q) = %q, want %q", tc.input, got, tc.expect)
			}
		})
	}
}

func TestNewSpanID(t *testing.T) {
	id1 := NewSpanID()
	id2 := NewSpanID()
	if id1 == id2 {
		t.Error("span IDs should be unique")
	}
	if len(id1) != 16 {
		t.Errorf("span ID should be 16 hex chars, got %d", len(id1))
	}
}

func TestMiddlewareTraceparent(t *testing.T) {
	// Test that the middleware extracts the trace ID from a traceparent header
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid := TraceIDFromContext(r.Context())
		if tid != "0af7651916cd43dd8448eb211c80319c" {
			t.Errorf("expected trace ID from traceparent, got %q", tid)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", http.NoBody)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7160204fbd-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func TestMiddlewareNoTraceparent(t *testing.T) {
	// Test that the middleware generates a trace ID when no traceparent is present
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid := TraceIDFromContext(r.Context())
		if tid == "" {
			t.Error("expected non-empty trace ID when no traceparent header")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func TestInjectTraceparent(t *testing.T) {
	ctx := WithTraceID(context.Background(), "0af7651916cd43dd8448eb211c80319c")
	req := httptest.NewRequest("GET", "http://example.com", http.NoBody)
	InjectTraceparent(ctx, req)

	tp := req.Header.Get("traceparent")
	if tp == "" {
		t.Fatal("expected traceparent header to be set")
	}
	// Should contain the trace ID
	if !strings.Contains(tp, "0af7651916cd43dd8448eb211c80319c") {
		t.Errorf("traceparent should contain trace ID, got %q", tp)
	}
	// Should be in W3C format: version-trace_id-parent_id-flags
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		t.Errorf("expected 4 parts in traceparent, got %d: %q", len(parts), tp)
	}
	if parts[0] != "00" {
		t.Errorf("expected version 00, got %q", parts[0])
	}
}

func TestInjectTraceparentNoTraceID(t *testing.T) {
	// When context has no trace ID, the header should not be set
	ctx := context.Background()
	req := httptest.NewRequest("GET", "http://example.com", http.NoBody)
	InjectTraceparent(ctx, req)

	if tp := req.Header.Get("traceparent"); tp != "" {
		t.Errorf("expected no traceparent header without trace ID, got %q", tp)
	}
}
