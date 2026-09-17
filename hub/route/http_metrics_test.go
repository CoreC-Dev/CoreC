package route

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestHTTPMetrics returns a fresh, isolated httpMetrics configured with
// the default latency buckets. Tests use it to exercise record/snapshot
// without touching the shared package-level httpMetricsCollector.
func newTestHTTPMetrics() *httpMetrics {
	return &httpMetrics{
		requestCount: make(map[string]uint64),
		durations: durationBuckets{
			buckets: defaultHTTPDurationBuckets,
			counts:  make([]uint64, len(defaultHTTPDurationBuckets)+1),
		},
	}
}

// TestHTTPMetricsRecordAndSnapshot verifies that record increments the
// method:status counter and places each observation into the correct
// histogram bucket, and that snapshot returns independent copies.
func TestHTTPMetricsRecordAndSnapshot(t *testing.T) {
	m := newTestHTTPMetrics()

	// bucket boundaries (seconds): 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5
	m.record("GET", http.StatusOK, 500*time.Microsecond)            // 0.0005s -> bucket 0
	m.record("GET", http.StatusOK, 8*time.Millisecond)              // 0.008s   -> bucket 2
	m.record("POST", http.StatusCreated, 2*time.Second)             // 2s       -> bucket 7
	m.record("GET", http.StatusInternalServerError, 10*time.Second) // 10s   -> overflow (8)

	counts, buckets, sum, count := m.snapshot()

	if counts["GET:200"] != 2 {
		t.Errorf("counts[GET:200] = %d, want 2", counts["GET:200"])
	}
	if counts["POST:201"] != 1 {
		t.Errorf("counts[POST:201] = %d, want 1", counts["POST:201"])
	}
	if counts["GET:500"] != 1 {
		t.Errorf("counts[GET:500] = %d, want 1", counts["GET:500"])
	}
	if count != 4 {
		t.Errorf("duration count = %d, want 4", count)
	}
	if buckets[0] != 1 {
		t.Errorf("bucket[0] (<=0.001s) = %d, want 1", buckets[0])
	}
	if buckets[2] != 1 {
		t.Errorf("bucket[2] (<=0.01s) = %d, want 1", buckets[2])
	}
	if buckets[7] != 1 {
		t.Errorf("bucket[7] (<=5s) = %d, want 1", buckets[7])
	}
	if buckets[8] != 1 {
		t.Errorf("overflow bucket (>5s) = %d, want 1", buckets[8])
	}
	// sum = 0.0005 + 0.008 + 2 + 10 = 12.0085s
	if sum < 12.0 || sum > 12.1 {
		t.Errorf("duration sum = %v, want ~12.0085", sum)
	}

	// snapshot must return independent copies: mutating the returned map
	// and slice must not affect subsequent snapshots.
	counts["GET:200"] = 999
	buckets[0] = 999
	rerun, _, _, _ := m.snapshot()
	if rerun["GET:200"] != 2 {
		t.Errorf("snapshot not independent: counts[GET:200] = %d after mutating returned map", rerun["GET:200"])
	}
}

// TestHTTPMetricsMiddlewareCountsRequests verifies the middleware counts
// requests by "method:status" and records durations into the shared
// httpMetricsCollector. Because the collector is shared across the whole
// test package (other tests route requests through router()), the test
// compares before/after snapshots (deltas) rather than absolute values.
func TestHTTPMetricsMiddlewareCountsRequests(t *testing.T) {
	beforeCounts, beforeBuckets, beforeSum, beforeCount := httpMetricsCollector.snapshot()

	okHandler := httpMetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		render(w, r, http.StatusOK, map[string]string{"ok": "true"})
	}))
	errHandler := httpMetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		renderError(w, r, http.StatusInternalServerError, "boom")
	}))

	// Two GET 200s.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/things", http.NoBody)
		rec := httptest.NewRecorder()
		okHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected status 200, got %d", i, rec.Code)
		}
	}

	// One POST 500.
	req := httptest.NewRequest(http.MethodPost, "/things", http.NoBody)
	rec := httptest.NewRecorder()
	errHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}

	afterCounts, afterBuckets, afterSum, afterCount := httpMetricsCollector.snapshot()

	if got := afterCounts["GET:200"] - beforeCounts["GET:200"]; got != 2 {
		t.Errorf("GET:200 delta = %d, want 2", got)
	}
	if got := afterCounts["POST:500"] - beforeCounts["POST:500"]; got != 1 {
		t.Errorf("POST:500 delta = %d, want 1", got)
	}
	if got := afterCount - beforeCount; got != 3 {
		t.Errorf("duration count delta = %d, want 3", got)
	}
	if afterSum <= beforeSum {
		t.Errorf("duration sum did not increase: before=%v after=%v", beforeSum, afterSum)
	}

	// The sum of bucket deltas must equal the number of recorded durations:
	// every observation lands in exactly one bucket.
	if len(beforeBuckets) != len(afterBuckets) {
		t.Fatalf("bucket count changed: before=%d after=%d", len(beforeBuckets), len(afterBuckets))
	}
	var bucketDelta uint64
	for i := range afterBuckets {
		bucketDelta += afterBuckets[i] - beforeBuckets[i]
	}
	if bucketDelta != 3 {
		t.Errorf("sum of bucket deltas = %d, want 3", bucketDelta)
	}
}

// TestHTTPMetricsMiddlewarePreservesResponse verifies the middleware does
// not alter the response status code, body, or Content-Type — it only
// observes. Uses a non-200 status to confirm pass-through of unusual codes.
func TestHTTPMetricsMiddlewarePreservesResponse(t *testing.T) {
	wantBody := map[string]string{"hello": "world"}
	h := httpMetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		render(w, r, http.StatusTeapot, wantBody)
	}))

	req := httptest.NewRequest(http.MethodGet, "/teapot", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d (middleware must not alter status)", rec.Code, http.StatusTeapot)
	}
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["hello"] != "world" {
		t.Errorf("body[hello] = %q, want %q", got["hello"], "world")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// The 418 must have been recorded under GET:418 (delta against the
	// shared collector, in case other tests ran first).
	before, _, _, _ := httpMetricsCollector.snapshot()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/teapot", http.NoBody))
	after, _, _, _ := httpMetricsCollector.snapshot()
	if got := after["GET:418"] - before["GET:418"]; got != 1 {
		t.Errorf("GET:418 delta = %d, want 1", got)
	}
}

// TestHTTPMetricsMiddlewareDefaultsStatusOnWrite verifies that a handler
// which writes a body without calling WriteHeader is recorded as 200,
// matching net/http's implicit-200 behavior.
func TestHTTPMetricsMiddlewareDefaultsStatusOnWrite(t *testing.T) {
	h := httpMetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Intentionally no WriteHeader: net/http would send 200.
		_, _ = w.Write([]byte("plain"))
	}))

	before, _, _, _ := httpMetricsCollector.snapshot()

	req := httptest.NewRequest(http.MethodGet, "/plain", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	after, _, _, _ := httpMetricsCollector.snapshot()
	if got := after["GET:200"] - before["GET:200"]; got != 1 {
		t.Errorf("GET:200 delta = %d, want 1 (implicit 200 on Write)", got)
	}
}
