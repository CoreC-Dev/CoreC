// Package route implements the CoreC RESTful API gateway: HTTP routing,
// middleware (CORS, rate limiting, authentication, request metrics), request
// handlers, WebSocket streams, and Prometheus-format metrics exposition.
package route

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// defaultHTTPDurationBuckets defines the upper bounds (in seconds) of the
// HTTP request-duration histogram. The tiers match common latency SLOs
// (1ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s, 5s). Observations that exceed
// the largest bucket fall into an implicit +Inf overflow bucket, so the
// counts slice has length len(defaultHTTPDurationBuckets)+1.
var defaultHTTPDurationBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5}

// durationBuckets is a minimal request-latency histogram. counts[i] holds
// the number of observations whose duration is <= buckets[i] and strictly
// greater than buckets[i-1] (buckets[-1] is treated as 0). counts[len(buckets)]
// is the overflow bucket for observations exceeding the largest boundary.
// sum accumulates the total duration in seconds and count the total number
// of observations, so the mean latency is sum/count.
type durationBuckets struct {
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

// httpMetrics collects HTTP request counts keyed by "method:status" and a
// coarse request-duration histogram. It is safe for concurrent use: record
// and snapshot both hold mu. The package-level httpMetricsCollector is
// populated by httpMetricsMiddleware and read by the /metrics handler.
type httpMetrics struct {
	mu           sync.Mutex
	requestCount map[string]uint64
	durations    durationBuckets
}

// record increments the request counter for the given "method:status" and
// folds the request duration into the histogram. Durations are stored in
// seconds (float64) to match Prometheus conventions.
func (m *httpMetrics) record(method string, status int, duration time.Duration) {
	key := method + ":" + strconv.Itoa(status)
	secs := duration.Seconds()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.requestCount[key]++

	db := &m.durations
	db.sum += secs
	db.count++

	// Place the observation into the first bucket whose upper bound is
	// >= the duration. If the duration exceeds every boundary, idx stays
	// at len(buckets) and the observation lands in the overflow bucket.
	idx := len(db.buckets)
	for i, ub := range db.buckets {
		if secs <= ub {
			idx = i
			break
		}
	}
	db.counts[idx]++
}

// snapshot returns point-in-time copies of the collected counters and
// histogram. The returned map and slice are independent copies, so mutating
// them does not affect the collector. Callers that render Prometheus output
// should hold the result only for the duration of rendering.
func (m *httpMetrics) snapshot() (counts map[string]uint64, durBuckets []uint64, durSum float64, durCount uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	counts = make(map[string]uint64, len(m.requestCount))
	for k, v := range m.requestCount {
		counts[k] = v
	}
	durBuckets = make([]uint64, len(m.durations.counts))
	copy(durBuckets, m.durations.counts)
	durSum = m.durations.sum
	durCount = m.durations.count
	return counts, durBuckets, durSum, durCount
}

// httpMetricsCollector is the package-level collector populated by
// httpMetricsMiddleware. The /metrics handler reads it to expose
// corec_http_requests_total and corec_http_request_duration_seconds. It is
// initialized with the default latency buckets and an empty request-count
// map so the first request never allocates the map.
var httpMetricsCollector = &httpMetrics{
	requestCount: make(map[string]uint64),
	durations: durationBuckets{
		buckets: defaultHTTPDurationBuckets,
		counts:  make([]uint64, len(defaultHTTPDurationBuckets)+1),
	},
}

// statusRecorder wraps an http.ResponseWriter to capture the HTTP status
// code of the response. It delegates Header, Write, and WriteHeader to the
// underlying writer and forwards the optional http.Hijacker and http.Flusher
// interfaces so that WebSocket upgrades (coder/websocket.Accept calls
// Hijack) and any flushing responses keep working through the middleware
// stack.
//
// The first non-informational WriteHeader call (or the first Write, which
// implies http.StatusOK) fixes the recorded status; duplicate WriteHeader
// calls are dropped, matching chi's WrapResponseWriter semantics. If neither
// WriteHeader nor Write is called, status() reports http.StatusOK — the
// status net/http sends by default for a no-op handler.
type statusRecorder struct {
	http.ResponseWriter
	code        int
	wroteHeader bool
}

// Compile-time assertions that statusRecorder satisfies the optional
// interfaces a middleware wrapper must preserve. Hijack is required for
// WebSocket upgrades; Flush preserves streaming/flush behavior.
var (
	_ http.Hijacker = (*statusRecorder)(nil)
	_ http.Flusher  = (*statusRecorder)(nil)
)

// WriteHeader records the first final status code and forwards it.
// Informational 1xx responses (except 101 Switching Protocols) are forwarded
// without counting as the final status, matching net/http and chi semantics.
func (r *statusRecorder) WriteHeader(code int) {
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	if r.wroteHeader {
		// Duplicate final-status WriteHeader; drop to match chi's dedup.
		return
	}
	r.code = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

// Write delegates to the underlying writer. A handler that writes a body
// without calling WriteHeader gets an implicit 200 from net/http; record it
// so the metrics reflect the status the client actually observed.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.code = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

// status returns the recorded HTTP status, defaulting to http.StatusOK when
// the handler wrote nothing (net/http's default).
func (r *statusRecorder) status() int {
	if !r.wroteHeader {
		return http.StatusOK
	}
	return r.code
}

// Hijack delegates to the underlying writer when it implements http.Hijacker.
// This is required for WebSocket upgrades: coder/websocket.Accept type-
// asserts http.Hijacker and calls Hijack, so failing to forward it would
// break every /traffic, /memory, /logs, and /tags/stream connection.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("httpMetricsMiddleware: underlying ResponseWriter does not implement http.Hijacker")
	}
	return hj.Hijack()
}

// Flush delegates to the underlying writer when it implements http.Flusher,
// preserving flush behavior for streaming responses.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// httpMetricsMiddleware wraps an http.Handler and records each request's
// method, response status code, and duration into httpMetricsCollector. It
// is intended to be registered early in the chi middleware stack (after
// CORS, before rate limiting) so that every request — including
// unauthenticated /healthz probes — is tracked for Prometheus exposure.
func httpMetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		httpMetricsCollector.record(r.Method, rec.status(), time.Since(start))
	})
}
