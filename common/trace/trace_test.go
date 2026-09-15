package trace

import (
	"context"
	"testing"
	"time"
)

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
	ctx := context.Background()
	ctx, span := Start(ctx, "test.operation")
	if span == nil {
		t.Fatal("span should not be nil")
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
	ctx := context.Background()
	traceID := "existing-trace-id"
	ctx = WithTraceID(ctx, traceID)
	_, span := Start(ctx, "child.operation")
	if span.traceID != traceID {
		t.Errorf("span should inherit trace ID %q, got %q", traceID, span.traceID)
	}
	span.End()
}

func TestContextWithTrace(t *testing.T) {
	ctx := context.Background()
	ctx, traceID := ContextWithTrace(ctx)
	if traceID == "" {
		t.Error("trace ID should not be empty")
	}
	if TraceIDFromContext(ctx) != traceID {
		t.Error("context should carry the trace ID")
	}

	// Second call should reuse existing trace ID
	ctx2, traceID2 := ContextWithTrace(ctx)
	if traceID2 != traceID {
		t.Error("ContextWithTrace should reuse existing trace ID")
	}
	_ = ctx2
}

func TestSpanDuration(t *testing.T) {
	ctx := context.Background()
	_, span := Start(ctx, "timed.operation")
	time.Sleep(2 * time.Millisecond)
	span.End()
	if !span.ended.Load() {
		t.Error("span should be marked as ended")
	}
}
