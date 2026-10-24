package route

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
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
		"# TYPE corec_mem_total_alloc_bytes counter",
		"# TYPE corec_gc_count counter",
		"# TYPE corec_gc_pause_total_seconds counter",
		"# HELP corec_cpu_count",
	}
	for _, s := range required {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
}

func TestPromMetricsWithDrivers(t *testing.T) {
	lastRead := time.Now().Add(-3 * time.Second)
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
					LastRead:   lastRead,
				},
				"opcua1": {
					Name:       "opcua1",
					Type:       "opcua",
					State:      core.StateDisconnected,
					ReadCount:  100,
					ErrorCount: 5,
					TagCount:   5,
					// LastRead zero: never completed a read — the
					// freshness series must be omitted for this driver
					// instead of reporting a fabricated age.
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

	// Freshness gauges: modbus1 read 3s ago — the
	// timestamp is exact and the age is ~3s (allow scheduling slack).
	wantTS := fmt.Sprintf("corec_driver_last_read_timestamp_seconds{driver=\"modbus1\"} %d", lastRead.Unix())
	if !strings.Contains(output, wantTS) {
		t.Errorf("metrics output missing %q\nOutput:\n%s", wantTS, output)
	}
	idx := strings.Index(output, "\ncorec_driver_last_read_seconds{driver=\"modbus1\"} ")
	if idx < 0 {
		t.Fatalf("metrics output missing corec_driver_last_read_seconds sample for modbus1\nOutput:\n%s", output)
	}
	// Sample line: name{labels} value — the value is the second field.
	ageField := strings.Fields(output[idx+1:])[1]
	age, err := strconv.ParseFloat(ageField, 64)
	if err != nil {
		t.Fatalf("failed to parse last_read_seconds value %q: %v", ageField, err)
	}
	if age < 2.5 || age > 10 {
		t.Errorf("last_read_seconds = %g, want ~3s (2.5..10 slack)", age)
	}

	// opcua1 never read: both freshness series must be absent.
	for _, absent := range []string{
		"corec_driver_last_read_timestamp_seconds{driver=\"opcua1\"}",
		"corec_driver_last_read_seconds{driver=\"opcua1\"}",
	} {
		if strings.Contains(output, absent) {
			t.Errorf("metrics output unexpectedly contains %q (never-read driver must omit the series)", absent)
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

// TestPromMetricsNewFamilies verifies that the observability enhancement
// metric families are present in the /metrics output: log dropped counter,
// driver reconnect counter, offline buffer metrics, HTTP request metrics,
// and latency histograms. The mockEngineV2 does not satisfy
// LatencyProvider or OfflineBufferStatsProvider, so those families are
// gracefully omitted — this test verifies the families that ARE emitted
// even without the optional role interfaces.
func TestPromMetricsNewFamilies(t *testing.T) {
	eng := &mockEngineV2{
		stats: core.EngineStats{
			TotalRead:    100,
			TotalPublish: 90,
			TotalErrors:  1,
			TotalDropped: 2,
			DriverStats: map[string]core.DriverStatus{
				"plc1": {Name: "plc1", Type: "modbus-tcp", ReadCount: 50, ErrorCount: 1, ReconnectCount: 3},
				"plc2": {Name: "plc2", Type: "s7", ReadCount: 50, ErrorCount: 0, ReconnectCount: 0},
			},
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

	checks := []string{
		// Log dropped counter
		"# HELP corec_log_dropped_total",
		"# TYPE corec_log_dropped_total counter",
		"corec_log_dropped_total",

		// Driver reconnect counter
		"# HELP corec_driver_reconnect_total",
		"# TYPE corec_driver_reconnect_total counter",
		`corec_driver_reconnect_total{driver="plc1"} 3`,
		`corec_driver_reconnect_total{driver="plc2"} 0`,

		// HTTP request metrics (the /metrics request itself is counted
		// by httpMetricsMiddleware, so at least one request exists)
		"# HELP corec_http_requests_total",
		"# TYPE corec_http_requests_total counter",
		"# HELP corec_http_request_duration_seconds",
		"# TYPE corec_http_request_duration_seconds histogram",
		"corec_http_request_duration_seconds_bucket",
		"corec_http_request_duration_seconds_sum",
		"corec_http_request_duration_seconds_count",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\n", s)
		}
	}

	// Verify that latency and offline buffer metrics are gracefully
	// absent when the engine does not implement the optional interfaces.
	mustNotExist := []string{
		"corec_read_latency_seconds",
		"corec_publish_latency_seconds",
		"corec_data_age_seconds",
		"corec_offline_buffer_pending",
		"corec_offline_buffer_drained_total",
		"corec_offline_buffer_pushed_total",
	}
	for _, s := range mustNotExist {
		if strings.Contains(output, s) {
			t.Errorf("metrics output should NOT contain %q (mock does not implement the provider interface)\n", s)
		}
	}
}

// mockLatencyEngine implements core.LatencyProvider and core.DataAgeProvider
// (but not the full core.Engine) to verify the histogram families are
// emitted when the optional role interfaces are satisfied.
type mockLatencyEngine struct {
	mockEngineV2
	readSnap    core.LatencySnapshot
	publishSnap core.LatencySnapshot
	dataAgeSnap core.LatencySnapshot
}

func (m *mockLatencyEngine) ReadLatencyHistogram() core.LatencySnapshot    { return m.readSnap }
func (m *mockLatencyEngine) PublishLatencyHistogram() core.LatencySnapshot { return m.publishSnap }
func (m *mockLatencyEngine) DataAgeHistogram() core.LatencySnapshot        { return m.dataAgeSnap }

// TestPromMetricsLatencyAndDataAge verifies that the read/publish latency
// and data-age histogram families are emitted when the engine satisfies the
// corresponding optional role interfaces, including HELP/TYPE headers and
// the +Inf bucket / sum / count lines.
func TestPromMetricsLatencyAndDataAge(t *testing.T) {
	eng := &mockLatencyEngine{
		mockEngineV2: mockEngineV2{},
		readSnap: core.LatencySnapshot{
			Buckets: []float64{0.1, 1},
			Counts:  []uint64{2, 5},
			Sum:     3.5,
			Count:   5,
		},
		publishSnap: core.LatencySnapshot{
			Buckets: []float64{0.1, 1},
			Counts:  []uint64{4, 5},
			Sum:     2.0,
			Count:   5,
		},
		dataAgeSnap: core.LatencySnapshot{
			Buckets: []float64{1, 60},
			Counts:  []uint64{3, 5},
			Sum:     120.0,
			Count:   5,
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

	// data-age histogram family must be fully present.
	dataAgeChecks := []string{
		"# HELP corec_data_age_seconds",
		"# TYPE corec_data_age_seconds histogram",
		`corec_data_age_seconds_bucket{le="1"} 3`,
		`corec_data_age_seconds_bucket{le="60"} 5`,
		`corec_data_age_seconds_bucket{le="+Inf"} 5`,
		"corec_data_age_seconds_sum 120",
		"corec_data_age_seconds_count 5",
	}
	for _, s := range dataAgeChecks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\n", s)
		}
	}

	// read/publish latency must also be present (sanity).
	for _, s := range []string{"corec_read_latency_seconds_count 5", "corec_publish_latency_seconds_count 5"} {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\n", s)
		}
	}
}

// TestPromMetricsSchedulerHealth verifies the scheduler health families
// render from the optional SchedulerHealthProvider role: a degraded
// driver with an error streak and cumulative
// overruns is visible to Prometheus, and an engine that does not report
// scheduler health (nil role data) omits the families entirely.
func TestPromMetricsSchedulerHealth(t *testing.T) {
	eng := &mockEngineV2{
		schedulerHealth: []core.TaskHealth{
			{Driver: "plc1", Degraded: true, ConsecutiveErrors: 6, OverrunsTotal: 42},
			{Driver: "plc2", Degraded: false, ConsecutiveErrors: 0, OverrunsTotal: 0},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	checks := []string{
		"corec_scheduler_degraded{driver=\"plc1\"} 1",
		"corec_scheduler_degraded{driver=\"plc2\"} 0",
		"corec_scheduler_consecutive_errors{driver=\"plc1\"} 6",
		"corec_scheduler_overruns_total{driver=\"plc1\"} 42",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}

	// A mock with no scheduler health must omit the families rather
	// than emitting headers with zero samples.
	engNone := &mockEngineV2{}
	ts2 := newTestServer("", engNone)
	defer ts2.Close()
	resp2 := authedGet(t, ts2, "/metrics", "")
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if strings.Contains(string(body2), "corec_scheduler_degraded") {
		t.Errorf("metrics output unexpectedly contains scheduler families for a health-less engine")
	}
}

// TestPromMetricsDeadLetter verifies the dead-letter families render from
// the optional DeadLetterStatsProvider role: queue depth
// and lifetime pushes must be scrapeable, not just queryable via
// /write/failed.
func TestPromMetricsDeadLetter(t *testing.T) {
	eng := &mockEngineV2{dlqSize: 4, dlqPushed: 12}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	checks := []string{
		"corec_dead_letter_queue_size 4",
		"corec_dead_letter_pushed_total 12",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
}

// TestPromMetricsLabeledLatency verifies the labeled histogram families
// render per-driver/per-transport series when the engine
// provides core.LabeledLatencyProvider, and that engines with only the
// plain LatencyProvider keep the unlabeled fallback.
type mockLabeledLatency struct {
	mockEngineV2
	byDriver       map[string]core.LatencySnapshot
	byTransport    map[string]core.LatencySnapshot
	byTransportAge map[string]core.LatencySnapshot
}

func (m *mockLabeledLatency) ReadLatencyByDriver() map[string]core.LatencySnapshot {
	return m.byDriver
}
func (m *mockLabeledLatency) PublishLatencyByTransport() map[string]core.LatencySnapshot {
	return m.byTransport
}
func (m *mockLabeledLatency) DataAgeByTransport() map[string]core.LatencySnapshot {
	return m.byTransportAge
}

func TestPromMetricsLabeledLatency(t *testing.T) {
	eng := &mockLabeledLatency{
		byDriver: map[string]core.LatencySnapshot{
			"plc1": {Buckets: []float64{0.01}, Counts: []uint64{7}, Sum: 0.05, Count: 10},
			"plc2": {Buckets: []float64{0.01}, Counts: []uint64{3}, Sum: 0.25, Count: 4},
		},
		byTransport: map[string]core.LatencySnapshot{
			"mqtt-a": {Buckets: []float64{0.01}, Counts: []uint64{9}, Sum: 0.02, Count: 12},
		},
		byTransportAge: map[string]core.LatencySnapshot{
			"mqtt-a": {Buckets: []float64{0.5}, Counts: []uint64{6}, Sum: 1.5, Count: 6},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	checks := []string{
		"corec_read_latency_seconds_bucket{driver=\"plc1\",le=\"0.01\"} 7",
		"corec_read_latency_seconds_bucket{driver=\"plc1\",le=\"+Inf\"} 10",
		"corec_read_latency_seconds_count{driver=\"plc2\"} 4",
		"corec_publish_latency_seconds_bucket{transport=\"mqtt-a\",le=\"0.01\"} 9",
		"corec_data_age_seconds_bucket{transport=\"mqtt-a\",le=\"0.5\"} 6",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
}

// TestPromMetricsUnlabeledLatencyFallback verifies engines that only
// implement core.LatencyProvider (no labeled role) still get the
// unlabeled families.
type mockUnlabeledLatency struct {
	mockEngineV2
	read    core.LatencySnapshot
	publish core.LatencySnapshot
}

func (m *mockUnlabeledLatency) ReadLatencyHistogram() core.LatencySnapshot    { return m.read }
func (m *mockUnlabeledLatency) PublishLatencyHistogram() core.LatencySnapshot { return m.publish }

func TestPromMetricsUnlabeledLatencyFallback(t *testing.T) {
	eng := &mockUnlabeledLatency{
		read:    core.LatencySnapshot{Buckets: []float64{0.01}, Counts: []uint64{7}, Sum: 0.05, Count: 10},
		publish: core.LatencySnapshot{Buckets: []float64{0.01}, Counts: []uint64{9}, Sum: 0.02, Count: 12},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	checks := []string{
		"corec_read_latency_seconds_bucket{le=\"0.01\"} 7",
		"corec_read_latency_seconds_count 10",
		"corec_publish_latency_seconds_bucket{le=\"0.01\"} 9",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
	// No labeled series may appear in the fallback path.
	if strings.Contains(output, "driver=\"") {
		t.Errorf("unlabeled fallback unexpectedly contains labeled series\nOutput:\n%s", output)
	}
}

// TestPromMetricsDroppedBreakdown verifies the per-cause drop families
// family by family: the breakdown must be scrapeable, not just diffable
// from the umbrella total.
func TestPromMetricsDroppedBreakdown(t *testing.T) {
	eng := &mockEngineV2{
		dropBreakdown: core.DroppedBreakdown{
			DataBusMain:     7,
			DataBusPriority: 3,
			SlowSubscriber:  12,
			BadQuality:      40,
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	checks := []string{
		"corec_databus_dropped_total{channel=\"main\"} 7",
		"corec_databus_dropped_total{channel=\"priority\"} 3",
		"corec_slow_subscriber_dropped_total 12",
		"corec_bad_quality_dropped_total 40",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
}

// TestPromMetricsDriverSeries verifies driver-emitted protocol metrics
// render under their driver-owned names (with the core.Metrics
// port wired only to NoopMetrics, emissions would vanish).
func TestPromMetricsDriverSeries(t *testing.T) {
	eng := &mockEngineV2{
		driverMetrics: []core.DriverMetric{
			{Name: "modbus_exception_total", Kind: "counter",
				Labels: map[string]string{"driver": "plc1", "code": "illegal-data-address"}, Value: 4},
			{Name: "modbus_exception_total", Kind: "counter",
				Labels: map[string]string{"driver": "plc2", "code": "server-device-failure"}, Value: 2},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	output := string(body)

	checks := []string{
		"# TYPE modbus_exception_total counter",
		"modbus_exception_total{code=\"illegal-data-address\",driver=\"plc1\"} 4",
		"modbus_exception_total{code=\"server-device-failure\",driver=\"plc2\"} 2",
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, output)
		}
	}
}
