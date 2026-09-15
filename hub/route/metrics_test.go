package route

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestPromMetricsBasic(t *testing.T) {
	eng := &mockEngineV2{
		stats: core.EngineStats{
			TotalRead:    1000,
			TotalPublish: 950,
			TotalErrors:  10,
			TotalDropped: 5,
			Drivers:      2,
			Transports:   1,
			Rules:        3,
			Uptime:       3600 * time.Second,
			PointsPerSec: 125.5,
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	// Verify Content-Type
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain Content-Type, got %q", ct)
	}

	// Verify required metric families are present
	required := []string{
		"# HELP corec_reads_total",
		"# TYPE corec_reads_total counter",
		"corec_reads_total 1000",
		"# HELP corec_publishes_total",
		"corec_publishes_total 950",
		"# HELP corec_errors_total",
		"corec_errors_total 10",
		"# HELP corec_dropped_total",
		"corec_dropped_total 5",
		"# HELP corec_drivers",
		"corec_drivers 2",
		"# HELP corec_transports",
		"corec_transports 1",
		"# HELP corec_rules",
		"corec_rules 3",
		"# HELP corec_uptime_seconds",
		"# HELP corec_points_per_second",
		// Runtime metrics
		"# HELP corec_goroutines",
		"# TYPE corec_goroutines gauge",
		"# HELP corec_mem_heap_alloc_bytes",
		"# HELP corec_gc_count",
		"# HELP corec_cpu_count",
	}
	for _, s := range required {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
}

func TestPromMetricsWithDrivers(t *testing.T) {
	eng := &mockEngineV2{
		stats: core.EngineStats{
			TotalRead: 500,
			DriverStats: map[string]core.DriverStatus{
				"modbus1": {
					Name:       "modbus1",
					Type:       "modbus-tcp",
					State:      core.StateConnected,
					ReadCount:  400,
					ErrorCount: 2,
					TagCount:   10,
				},
				"opcua1": {
					Name:       "opcua1",
					Type:       "opcua",
					State:      core.StateDisconnected,
					ReadCount:  100,
					ErrorCount: 5,
					TagCount:   5,
				},
			},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	// Verify per-driver metrics
	checks := []string{
		"corec_driver_read_total{driver=\"modbus1\",type=\"modbus-tcp\"} 400",
		"corec_driver_read_total{driver=\"opcua1\",type=\"opcua\"} 100",
		"corec_driver_errors_total{driver=\"modbus1\"} 2",
		"corec_driver_errors_total{driver=\"opcua1\"} 5",
		"corec_driver_tags{driver=\"modbus1\"} 10",
		"corec_driver_connected{driver=\"modbus1\"} 1",
		"corec_driver_connected{driver=\"opcua1\"} 0",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
}

func TestPromMetricsWithTransports(t *testing.T) {
	eng := &mockEngineV2{
		stats: core.EngineStats{
			TransportStats: map[string]core.TransportStatus{
				"cloud-mqtt": {
					Name:      "cloud-mqtt",
					Type:      "mqtt",
					State:     core.StateConnected,
					Published: 800,
					Failed:    3,
					Received:  100,
					QueueSize: 15,
				},
			},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	checks := []string{
		"corec_transport_published_total{transport=\"cloud-mqtt\",type=\"mqtt\"} 800",
		"corec_transport_failed_total{transport=\"cloud-mqtt\"} 3",
		"corec_transport_received_total{transport=\"cloud-mqtt\"} 100",
		"corec_transport_queue_size{transport=\"cloud-mqtt\"} 15",
		"corec_transport_connected{transport=\"cloud-mqtt\"} 1",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
}

func TestPromMetricsRequiresAuth(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("secret123", eng)
	defer ts.Close()

	// Without auth should fail
	resp := authedGet(t, ts, "/metrics", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// With auth should succeed
	resp = authedGet(t, ts, "/metrics", "secret123")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with auth, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPromEscape(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"plain", "plain"},
		{`with"quote`, `with\"quote`},
		{`with\backslash`, `with\\backslash`},
		{"with\nnewline", "with\\nnewline"},
	}
	for _, tc := range tests {
		got := promEscape(tc.input)
		if got != tc.expect {
			t.Errorf("promEscape(%q) = %q, want %q", tc.input, got, tc.expect)
		}
	}
}
