package route

import (
	"encoding/json"
	"net/http"
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
