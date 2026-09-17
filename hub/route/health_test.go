package route

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// healthBody is the JSON shape returned by the /healthz/* endpoints.
type healthBody struct {
	Status string `json:"status"`
}

// decodeHealth reads and JSON-decodes a /healthz/* response body.
func decodeHealth(t *testing.T, resp *http.Response) healthBody {
	t.Helper()
	defer resp.Body.Close()
	var hb healthBody
	if err := json.NewDecoder(resp.Body).Decode(&hb); err != nil {
		t.Fatalf("decode health body failed: %v", err)
	}
	return hb
}

// TestHealthzLiveAlways200 verifies the liveness probe always returns 200
// regardless of engine state — it only signals that the process is alive.
func TestHealthzLiveAlways200(t *testing.T) {
	// Even with a fully stopped/empty engine, liveness must be 200.
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{Status: core.EngineStatusStopped},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/live")
	if err != nil {
		t.Fatalf("GET /healthz/live failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	hb := decodeHealth(t, resp)
	if hb.Status != "alive" {
		t.Errorf("expected status=alive, got %q", hb.Status)
	}
}

// TestHealthzReadyRunningWithConnectedDrivers verifies readiness returns 200
// when the engine is running and at least one driver is connected.
func TestHealthzReadyRunningWithConnectedDrivers(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:     core.EngineStatusRunning,
			Drivers:    2,
			Transports: 0,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
			{Name: "plc2", State: core.StateDisconnected},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	hb := decodeHealth(t, resp)
	if hb.Status != "ready" {
		t.Errorf("expected status=ready, got %q", hb.Status)
	}
}

// TestHealthzReadyStoppedEngine verifies readiness returns 503 when the
// engine is stopped.
func TestHealthzReadyStoppedEngine(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:  core.EngineStatusStopped,
			Drivers: 1,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	hb := decodeHealth(t, resp)
	if hb.Status != "not_ready" {
		t.Errorf("expected status=not_ready, got %q", hb.Status)
	}
}

// TestHealthzReadySuspendedEngine verifies readiness returns 503 when the
// engine is suspended (not running).
func TestHealthzReadySuspendedEngine(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:  core.EngineStatusSuspended,
			Drivers: 1,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

// TestHealthzReadyAllDriversDisconnected verifies readiness returns 503
// when drivers are configured but none are connected.
func TestHealthzReadyAllDriversDisconnected(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:  core.EngineStatusRunning,
			Drivers: 2,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateDisconnected},
			{Name: "plc2", State: core.StateError},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	hb := decodeHealth(t, resp)
	if hb.Status != "not_ready" {
		t.Errorf("expected status=not_ready, got %q", hb.Status)
	}
}

// TestHealthzReadyNoDriversVacuouslyReady verifies readiness returns 200
// when no drivers are configured (vacuously ready).
func TestHealthzReadyNoDriversVacuouslyReady(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:  core.EngineStatusRunning,
			Drivers: 0,
		},
		drivers: nil,
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (vacuously ready), got %d", resp.StatusCode)
	}
	hb := decodeHealth(t, resp)
	if hb.Status != "ready" {
		t.Errorf("expected status=ready, got %q", hb.Status)
	}
}

// TestHealthzReadyTransportsDisconnected verifies readiness returns 503
// when transports are configured but none are connected, even if drivers
// are healthy.
func TestHealthzReadyTransportsDisconnected(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:     core.EngineStatusRunning,
			Drivers:    1,
			Transports: 1,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
		},
		transports: []core.TransportStatus{
			{Name: "mqtt1", State: core.StateDisconnected},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

// TestHealthzReadyTransportsConnected verifies readiness returns 200 when
// both drivers and transports are configured and at least one of each is
// connected.
func TestHealthzReadyTransportsConnected(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:     core.EngineStatusRunning,
			Drivers:    1,
			Transports: 1,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
		},
		transports: []core.TransportStatus{
			{Name: "mqtt1", State: core.StateConnected},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// TestHealthEndpointsNoAuth verifies that both health endpoints are
// reachable WITHOUT an Authorization header even when an API secret is
// configured. Kubernetes probes do not carry auth headers, so these
// endpoints must live outside the auth group.
func TestHealthEndpointsNoAuth(t *testing.T) {
	// Configure a non-empty secret so the auth group is active.
	ts := newTestServer("super-secret", &mockEngineV2{
		stats: core.EngineStats{
			Status:  core.EngineStatusRunning,
			Drivers: 1,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
		},
	})
	defer ts.Close()

	client := ts.Client()

	// Liveness without auth -> 200
	resp, err := client.Get(ts.URL + "/healthz/live")
	if err != nil {
		t.Fatalf("GET /healthz/live failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("liveness without auth: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Readiness without auth -> 200
	resp, err = client.Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readiness without auth: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Sanity check: an authenticated endpoint WITHOUT auth must still
	// return 401, proving the secret is actually enforced on other routes
	// and only the health endpoints bypass it.
	resp, err = client.Get(ts.URL + "/drivers")
	if err != nil {
		t.Fatalf("GET /drivers failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for /drivers without auth, got %d", resp.StatusCode)
	}
}

// decodeHealthNotReady reads and JSON-decodes a not-ready /healthz/ready
// response body into the structured notReadyHealthResponse shape.
func decodeHealthNotReady(t *testing.T, resp *http.Response) notReadyHealthResponse {
	t.Helper()
	defer resp.Body.Close()
	var nr notReadyHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&nr); err != nil {
		t.Fatalf("decode not-ready body failed: %v", err)
	}
	return nr
}

// TestHealthzReadyComponentDetailsAllDriversDisconnected verifies that
// when readiness fails because no drivers are connected, the response
// includes a reason and a per-driver components list with the correct
// connected flags. This guards the Part A enhancement contract.
func TestHealthzReadyComponentDetailsAllDriversDisconnected(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:  core.EngineStatusRunning,
			Drivers: 2,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateDisconnected},
			{Name: "plc2", State: core.StateError},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	nr := decodeHealthNotReady(t, resp)
	if nr.Status != "not_ready" {
		t.Errorf("expected status=not_ready, got %q", nr.Status)
	}
	if nr.Reason != "no drivers connected" {
		t.Errorf("expected reason='no drivers connected', got %q", nr.Reason)
	}
	if len(nr.Components.Drivers) != 2 {
		t.Fatalf("expected 2 driver components, got %d", len(nr.Components.Drivers))
	}
	for i, want := range []struct {
		name      string
		connected bool
	}{
		{"plc1", false}, {"plc2", false},
	} {
		got := nr.Components.Drivers[i]
		if got.Name != want.name || got.Connected != want.connected {
			t.Errorf("driver[%d] = {name:%q connected:%v}, want {name:%q connected:%v}",
				i, got.Name, got.Connected, want.name, want.connected)
		}
	}
	// Transports slice must be present and empty (none configured).
	if nr.Components.Transports == nil {
		t.Error("expected transports slice to be non-nil (empty array), got nil")
	}
	if len(nr.Components.Transports) != 0 {
		t.Errorf("expected 0 transport components, got %d", len(nr.Components.Transports))
	}
}

// TestHealthzReadyComponentDetailsTransportsDisconnected verifies the
// components list reports correct connected flags for a mixed driver set
// (one connected, one not) when readiness fails on the transport side.
func TestHealthzReadyComponentDetailsTransportsDisconnected(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:     core.EngineStatusRunning,
			Drivers:    2,
			Transports: 1,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
			{Name: "plc2", State: core.StateDisconnected},
		},
		transports: []core.TransportStatus{
			{Name: "mqtt1", State: core.StateDisconnected},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	nr := decodeHealthNotReady(t, resp)
	if nr.Reason != "no transports connected" {
		t.Errorf("expected reason='no transports connected', got %q", nr.Reason)
	}
	// Drivers: plc1 connected=true, plc2 connected=false.
	if len(nr.Components.Drivers) != 2 {
		t.Fatalf("expected 2 driver components, got %d", len(nr.Components.Drivers))
	}
	if nr.Components.Drivers[0].Name != "plc1" || !nr.Components.Drivers[0].Connected {
		t.Errorf("expected plc1 connected=true, got %+v", nr.Components.Drivers[0])
	}
	if nr.Components.Drivers[1].Name != "plc2" || nr.Components.Drivers[1].Connected {
		t.Errorf("expected plc2 connected=false, got %+v", nr.Components.Drivers[1])
	}
	// Transports: mqtt1 connected=false.
	if len(nr.Components.Transports) != 1 {
		t.Fatalf("expected 1 transport component, got %d", len(nr.Components.Transports))
	}
	if nr.Components.Transports[0].Name != "mqtt1" || nr.Components.Transports[0].Connected {
		t.Errorf("expected mqtt1 connected=false, got %+v", nr.Components.Transports[0])
	}
}

// TestHealthzReadyComponentDetailsEngineNotRunning verifies the not-ready
// response carries a status reason and the configured components when the
// engine itself is stopped.
func TestHealthzReadyComponentDetailsEngineNotRunning(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:  core.EngineStatusStopped,
			Drivers: 1,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	nr := decodeHealthNotReady(t, resp)
	if nr.Status != "not_ready" {
		t.Errorf("expected status=not_ready, got %q", nr.Status)
	}
	if !strings.Contains(nr.Reason, "engine not running") {
		t.Errorf("expected reason to mention 'engine not running', got %q", nr.Reason)
	}
	if len(nr.Components.Drivers) != 1 || nr.Components.Drivers[0].Name != "plc1" {
		t.Errorf("expected plc1 in components, got %+v", nr.Components.Drivers)
	}
}

// TestHealthzReadyComponentDetailsEngineNil verifies the not-ready
// response shape is stable (empty component arrays, no nil fields) when
// the engine has not been initialized at all.
func TestHealthzReadyComponentDetailsEngineNil(t *testing.T) {
	ts := newTestServer("", nil)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	nr := decodeHealthNotReady(t, resp)
	if nr.Status != "not_ready" {
		t.Errorf("expected status=not_ready, got %q", nr.Status)
	}
	if nr.Reason != "engine not initialized" {
		t.Errorf("expected reason='engine not initialized', got %q", nr.Reason)
	}
	if nr.Components.Drivers == nil || nr.Components.Transports == nil {
		t.Error("expected empty-but-non-nil component slices, got nil")
	}
	if len(nr.Components.Drivers) != 0 || len(nr.Components.Transports) != 0 {
		t.Errorf("expected empty component slices, got drivers=%d transports=%d",
			len(nr.Components.Drivers), len(nr.Components.Transports))
	}
}

// TestHealthzReadyReadyBodyIsMinimal verifies that a ready probe returns
// only {"status":"ready"} with no reason or components fields, keeping
// the high-frequency probe response small.
func TestHealthzReadyReadyBodyIsMinimal(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{
		stats: core.EngineStats{
			Status:  core.EngineStatusRunning,
			Drivers: 1,
		},
		drivers: []core.DriverStatus{
			{Name: "plc1", State: core.StateConnected},
		},
	})
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/healthz/ready")
	if err != nil {
		t.Fatalf("GET /healthz/ready failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	// The ready path writes the exact bytes below with no trailing newline
	// and no component/reason fields.
	const want = `{"status":"ready"}`
	if string(body) != want {
		t.Errorf("ready body = %q, want %q (no reason/components)", string(body), want)
	}
}

// TestReadinessReasonUnit is a small unit test for the readinessReason
// helper, covering each branch without going through HTTP. This keeps the
// decision logic pinned independently of the handler wiring.
func TestReadinessReasonUnit(t *testing.T) {
	tests := []struct {
		name       string
		stats      core.EngineStats
		drivers    []core.DriverStatus
		transports []core.TransportStatus
		want       string
		wantReady  bool
	}{
		{
			name:      "stopped engine",
			stats:     core.EngineStats{Status: core.EngineStatusStopped, Drivers: 1},
			drivers:   []core.DriverStatus{{Name: "plc1", State: core.StateConnected}},
			wantReady: false,
		},
		{
			name:      "running no drivers vacuously ready",
			stats:     core.EngineStats{Status: core.EngineStatusRunning},
			want:      "",
			wantReady: true,
		},
		{
			name:      "running drivers all disconnected",
			stats:     core.EngineStats{Status: core.EngineStatusRunning, Drivers: 2},
			drivers:   []core.DriverStatus{{Name: "plc1", State: core.StateDisconnected}},
			want:      "no drivers connected",
			wantReady: false,
		},
		{
			name:       "running transports all disconnected",
			stats:      core.EngineStats{Status: core.EngineStatusRunning, Drivers: 1, Transports: 1},
			drivers:    []core.DriverStatus{{Name: "plc1", State: core.StateConnected}},
			transports: []core.TransportStatus{{Name: "mqtt1", State: core.StateDisconnected}},
			want:       "no transports connected",
			wantReady:  false,
		},
		{
			name:       "running with connected driver and transport",
			stats:      core.EngineStats{Status: core.EngineStatusRunning, Drivers: 1, Transports: 1},
			drivers:    []core.DriverStatus{{Name: "plc1", State: core.StateConnected}},
			transports: []core.TransportStatus{{Name: "mqtt1", State: core.StateConnected}},
			want:       "",
			wantReady:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := readinessReason(tc.stats, tc.drivers, tc.transports)
			if tc.wantReady && got != "" {
				t.Errorf("expected ready (empty reason), got %q", got)
			}
			if !tc.wantReady && got == "" {
				t.Errorf("expected a non-empty reason, got empty")
			}
			if tc.want != "" && got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}
