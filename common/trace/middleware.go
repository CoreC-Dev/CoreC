package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

// Middleware injects a trace ID into the request context. It implements
// W3C Trace Context propagation by parsing the incoming `traceparent`
// header (format: version-trace_id-parent_id-trace_flags). If a valid
// traceparent is present, its trace-id is reused so that this service's
// logs and spans are correlated with the upstream caller's trace. If no
// traceparent is present, it falls back to the chi request ID, and if
// that is also empty, generates a new W3C-compliant trace ID.
//
// The trace ID is available via trace.TraceIDFromContext(r.Context()) in
// downstream handlers and is automatically included in slog records when
// using a context-aware logger.
//
// Outbound HTTP requests can propagate the trace context by using
// trace.InjectTraceparent(ctx, req) before sending the request.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Try W3C traceparent header first: "version-trace_id-parent_id-trace_flags"
		if tp := r.Header.Get("traceparent"); tp != "" {
			if traceID := parseTraceparent(tp); traceID != "" {
				ctx = WithTraceID(ctx, traceID)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		// Fall back to chi RequestID middleware
		if reqID := middleware.GetReqID(ctx); reqID != "" {
			ctx = WithTraceID(ctx, reqID)
		} else {
			ctx = WithTraceID(ctx, NewTraceID())
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// parseTraceparent extracts the trace-id from a W3C traceparent header
// value. The format is: version-trace_id-parent_id-trace_flags where
// trace_id is 32 lowercase hex characters. Returns "" if the header is
// malformed.
func parseTraceparent(tp string) string {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return ""
	}
	// version must be "00" for the current spec
	if parts[0] != "00" {
		return ""
	}
	traceID := parts[1]
	if len(traceID) != 32 {
		return ""
	}
	// Validate hex
	for _, c := range traceID {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	// All-zero trace-id is invalid per W3C spec
	if traceID == "00000000000000000000000000000000" {
		return ""
	}
	return traceID
}

// InjectTraceparent sets the traceparent header on an outbound HTTP
// request so that the trace context propagates across service boundaries.
// It uses the trace ID from the context and generates a new parent span
// ID for this hop. If no trace ID is in the context, the header is not
// set (the caller's trace context is not invented).
func InjectTraceparent(ctx context.Context, req *http.Request) {
	traceID := TraceIDFromContext(ctx)
	if traceID == "" {
		return
	}
	// Generate a new 8-byte parent span ID (16 hex chars)
	spanID := NewSpanID()
	// version 00, trace-flags 01 (sampled)
	req.Header.Set("traceparent", "00-"+traceID+"-"+spanID+"-01")
}

// NewSpanID generates an 8-byte hex-encoded span ID (16 chars),
// matching W3C Trace Context format for the parent-id field.
func NewSpanID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}
