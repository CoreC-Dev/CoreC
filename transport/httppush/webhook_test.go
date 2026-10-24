package httppush

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// freePort returns a port that is currently free by letting the OS
// choose one, then closing the listener. There is a small race window
// before the caller rebinds, but it is far more robust than hardcoding.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// waitForServer polls the address until it accepts a connection or times out.
func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready", addr)
}

// TestWebhookOnData verifies the HTTP chained-core inbound path:
// POST to webhook → parser → OnData channel.
func TestWebhookOnData(t *testing.T) {
	addr := freePort(t)
	config := core.TransportConfig{
		Name: "test-webhook",
		Type: "http",
		Settings: map[string]any{
			"url":            "http://localhost:9999/no-such-server",
			"webhook-addr":   addr,
			"webhook-path":   "/data",
			"webhook-secret": "test-secret",
			// default parser = json.Unmarshal(DataPoint)
		},
	}

	transport, err := NewHTTPTransport(config)
	if err != nil {
		t.Fatal(err)
	}

	if err := transport.Init(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	if err := transport.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer transport.Stop()

	// OnData should be non-nil since webhook-addr is configured
	dataCh := transport.OnData()
	if dataCh == nil {
		t.Fatal("OnData returned nil despite webhook-addr being configured")
	}

	// Wait for webhook server to be ready
	waitForServer(t, addr)

	// POST a single DataPoint
	dp := core.DataPoint{
		Driver:    "upstream",
		Tag:       "temp",
		Value:     25.5,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}
	payload, _ := json.Marshal(dp)

	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/data", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	// Read from OnData channel
	select {
	case received := <-dataCh:
		if received.Tag != "temp" {
			t.Errorf("expected tag 'temp', got %q", received.Tag)
		}
		if v, ok := received.Value.(float64); !ok || v != 25.5 {
			t.Errorf("expected value 25.5, got %v", received.Value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no data point received from OnData")
	}

	// Verify Received counter
	status := transport.Status()
	if status.Received != 1 {
		t.Errorf("expected Received=1, got %d", status.Received)
	}
}

// TestWebhookOnDataArray verifies that an array of DataPoints is handled.
func TestWebhookOnDataArray(t *testing.T) {
	addr := freePort(t)
	config := core.TransportConfig{
		Name: "test-webhook-array",
		Type: "http",
		Settings: map[string]any{
			"url":            "http://localhost:9999/no-such-server",
			"webhook-addr":   addr,
			"webhook-path":   "/ingest",
			"webhook-secret": "test-secret",
		},
	}

	transport, err := NewHTTPTransport(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Init(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if err := transport.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer transport.Stop()

	dataCh := transport.OnData()

	// Wait for webhook server to be ready
	waitForServer(t, addr)

	// POST an array of DataPoints
	points := []core.DataPoint{
		{Driver: "d1", Tag: "t1", Value: 1.0, Type: core.TypeFloat64, Timestamp: time.Now()},
		{Driver: "d2", Tag: "t2", Value: 2.0, Type: core.TypeFloat64, Timestamp: time.Now()},
	}
	payload, _ := json.Marshal(points)

	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/ingest", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	// Should receive both points
	for i := 0; i < 2; i++ {
		select {
		case <-dataCh:
			// good
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout: only received %d of 2 points", i)
		}
	}

	status := transport.Status()
	if status.Received != 2 {
		t.Errorf("expected Received=2, got %d", status.Received)
	}
}

// TestNoWebhookOnDataNil verifies that OnData returns nil when no webhook is configured.
func TestNoWebhookOnDataNil(t *testing.T) {
	config := core.TransportConfig{
		Name: "test-no-webhook",
		Type: "http",
		Settings: map[string]any{
			"url": "http://localhost:9999/no-such-server",
		},
	}

	transport, err := NewHTTPTransport(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Init(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	if transport.OnData() != nil {
		t.Error("OnData should return nil when webhook-addr is not configured")
	}
}

// TestWebhookBatchOutcomeCounts pins the batch outcome counts: the batch
// response body carries per-outcome counts so partial failures are
// visible to the sender — parse failures and queue drops are counted
// distinctly instead of disappearing under a bare 202.
func TestWebhookBatchOutcomeCounts(t *testing.T) {
	addr := freePort(t)
	config := core.TransportConfig{
		Name: "test-webhook-counts",
		Type: "http",
		Settings: map[string]any{
			"url":            "http://localhost:9999/no-such-server",
			"webhook-addr":   addr,
			"webhook-path":   "/ingest",
			"webhook-secret": "test-secret",
		},
	}
	transport, err := NewHTTPTransport(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Init(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if err := transport.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer transport.Stop()

	waitForServer(t, addr)

	// Mixed array: two valid points and two malformed elements. A bare
	// string and a bare number both fail the unmarshal into DataPoint
	// (an object with only unknown fields would unmarshal to a zero
	// DataPoint — valid JSON, not a parse failure).
	payload := `[` +
		`{"driver":"d1","tag":"t1","value":1.0,"type":"float64","timestamp":"` + time.Now().Format(time.RFC3339Nano) + `"},` +
		`123,` +
		`"not-even-an-object",` +
		`{"driver":"d2","tag":"t2","value":2.0,"type":"float64","timestamp":"` + time.Now().Format(time.RFC3339Nano) + `"}]`
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/ingest", bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	var counts struct {
		Received     int `json:"received"`
		Accepted     int `json:"accepted"`
		ParseFailed  int `json:"parse_failed"`
		QueueDropped int `json:"queue_dropped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&counts); err != nil {
		t.Fatalf("decode counts: %v", err)
	}
	if counts.Received != 4 || counts.Accepted != 2 || counts.ParseFailed != 2 {
		t.Errorf("counts = received:%d accepted:%d parse_failed:%d dropped:%d, want 4/2/2/0",
			counts.Received, counts.Accepted, counts.ParseFailed, counts.QueueDropped)
	}
}
