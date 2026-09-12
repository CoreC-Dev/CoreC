package httppush

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestHTTPTransportPublish(t *testing.T) {
	var receivedCount atomic.Int32
	var lastAuthHeader string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuthHeader = r.Header.Get("Authorization")
		var points []core.DataPoint
		if err := json.NewDecoder(r.Body).Decode(&points); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedCount.Add(int32(len(points)))
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := core.TransportConfig{
		Name: "test-http",
		Type: "http",
		Settings: map[string]any{
			"url": ts.URL,
			"headers": map[string]any{
				"Authorization": "Bearer secret-token",
			},
			"timeout": "2s",
		},
	}

	tr, err := NewHTTPTransport(cfg)
	if err != nil {
		t.Fatalf("NewHTTPTransport failed: %v", err)
	}

	ctx := context.Background()
	if err := tr.Init(ctx, cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := tr.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer tr.Stop()

	point1 := core.DataPoint{
		Driver:    "line1",
		Tag:       "temp",
		Value:     45.6,
		Timestamp: time.Now(),
	}

	if err := tr.Publish(ctx, point1); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	if receivedCount.Load() != 1 {
		t.Errorf("expected 1 received point, got %d", receivedCount.Load())
	}
	if lastAuthHeader != "Bearer secret-token" {
		t.Errorf("expected Authorization header 'Bearer secret-token', got %q", lastAuthHeader)
	}
}
