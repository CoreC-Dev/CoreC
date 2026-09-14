package httppush

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// newStatusServer returns a test server that always responds with the
// given status and increments hits on each request.
func newStatusServer(hits *atomic.Int32, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
}

// newFlakyServer returns a test server that responds with the given
// sequence of status codes (one per request) and increments hits.
func newFlakyServer(hits *atomic.Int32, statuses []int) *httptest.Server {
	var idx atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		i := int(idx.Add(1)) - 1
		if i >= len(statuses) {
			i = len(statuses) - 1
		}
		w.WriteHeader(statuses[i])
	}))
}

// newHTTPTransportWithServer builds an HTTP transport pointing at the
// given test server URL with the given RetryCount, Inits and Starts it.
func newHTTPTransportWithServer(t *testing.T, name, url string, retryCount int) core.Transport {
	t.Helper()
	cfg := core.TransportConfig{
		Name:       name,
		Type:       "http",
		RetryCount: retryCount,
		Settings: map[string]any{
			"url":     url,
			"timeout": "2s",
		},
	}
	tr, err := NewHTTPTransport(cfg)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	if err := tr.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return tr
}

func samplePoint() core.DataPoint {
	return core.DataPoint{Driver: "d", Tag: "t", Value: 1.0, Type: core.TypeFloat64, Timestamp: time.Now()}
}

// TestRetryCountZeroNoRetry verifies that with RetryCount=0 (default) a
// 5xx response is reported immediately with a single attempt.
func TestRetryCountZeroNoRetry(t *testing.T) {
	var hits atomic.Int32
	srv := newStatusServer(&hits, http.StatusServiceUnavailable)
	defer srv.Close()

	tr := newHTTPTransportWithServer(t, "retry0", srv.URL, 0)
	defer tr.Stop()

	err := tr.PublishBatch(context.Background(), []core.DataPoint{samplePoint()})
	if err == nil {
		t.Fatal("expected error for 503 with RetryCount=0")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention status 503, got %q", err.Error())
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 attempt with RetryCount=0, got %d", got)
	}
}

// TestRetryOn5xx verifies that a 5xx response is retried up to
// RetryCount times (RetryCount+1 total attempts) before failing.
func TestRetryOn5xx(t *testing.T) {
	var hits atomic.Int32
	srv := newStatusServer(&hits, http.StatusServiceUnavailable)
	defer srv.Close()

	tr := newHTTPTransportWithServer(t, "retry5xx", srv.URL, 2)
	defer tr.Stop()

	err := tr.PublishBatch(context.Background(), []core.DataPoint{samplePoint()})
	if err == nil {
		t.Fatal("expected error after exhausting retries on 503")
	}
	if got := hits.Load(); got != 3 { // 1 initial + 2 retries
		t.Errorf("expected 3 attempts (RetryCount=2), got %d", got)
	}
}

// TestRetrySucceedsAfter5xx verifies that a transient 5xx followed by
// success is eventually published.
func TestRetrySucceedsAfter5xx(t *testing.T) {
	var hits atomic.Int32
	srv := newFlakyServer(&hits, []int{http.StatusServiceUnavailable, http.StatusOK})
	defer srv.Close()

	tr := newHTTPTransportWithServer(t, "retry-ok", srv.URL, 2)
	defer tr.Stop()

	if err := tr.PublishBatch(context.Background(), []core.DataPoint{samplePoint()}); err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("expected 2 attempts (fail then succeed), got %d", got)
	}
}

// TestNoRetryOn4xx verifies that a 4xx response is not retried (client
// errors will not change with retry).
func TestNoRetryOn4xx(t *testing.T) {
	var hits atomic.Int32
	srv := newStatusServer(&hits, http.StatusBadRequest)
	defer srv.Close()

	tr := newHTTPTransportWithServer(t, "retry4xx", srv.URL, 3)
	defer tr.Stop()

	err := tr.PublishBatch(context.Background(), []core.DataPoint{samplePoint()})
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status 400, got %q", err.Error())
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 attempt for 4xx (no retry), got %d", got)
	}
}

// TestRetryOnNetworkError verifies that a network error (unreachable
// server) is retried and ultimately reported.
func TestRetryOnNetworkError(t *testing.T) {
	srv := newStatusServer(new(atomic.Int32), http.StatusOK)
	url := srv.URL
	srv.Close() // make the URL unreachable

	tr := newHTTPTransportWithServer(t, "retry-net", url, 2)
	defer tr.Stop()

	err := tr.PublishBatch(context.Background(), []core.DataPoint{samplePoint()})
	if err == nil {
		t.Fatal("expected error after retries on unreachable server")
	}
	if !strings.Contains(err.Error(), "http push error") {
		t.Errorf("error should mention http push error, got %q", err.Error())
	}
}

// TestRetryRespectsContextCancel verifies that a cancelled context aborts
// the retry loop rather than sleeping through the full backoff.
func TestRetryRespectsContextCancel(t *testing.T) {
	var hits atomic.Int32
	srv := newStatusServer(&hits, http.StatusServiceUnavailable)
	defer srv.Close()

	tr := newHTTPTransportWithServer(t, "retry-cancel", srv.URL, 5)
	defer tr.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first attempt fails, before the long
	// backoff sleeps complete.
	go func() {
		time.Sleep(600 * time.Millisecond)
		cancel()
	}()

	err := tr.PublishBatch(ctx, []core.DataPoint{samplePoint()})
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	// The loop must have stopped well before the full retry sequence.
	if got := hits.Load(); got > 2 {
		t.Errorf("expected at most 2 attempts before cancellation, got %d", got)
	}
}
