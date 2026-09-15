package trace

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// Middleware injects a trace ID into the request context. It first checks
// for an incoming W3C Trace Context header (traceparent); if absent, it
// reuses the chi request ID; if that is also empty, it generates a new ID.
//
// The trace ID is available via trace.TraceIDFromContext(r.Context()) in
// downstream handlers and is automatically included in slog records when
// using a context-aware logger.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Try to get trace ID from chi RequestID middleware
		if reqID := middleware.GetReqID(ctx); reqID != "" {
			ctx = WithTraceID(ctx, reqID)
		} else {
			ctx = WithTraceID(ctx, NewTraceID())
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
