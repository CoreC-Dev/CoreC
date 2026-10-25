package hub

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/hub/route"
)

// freeAddr returns a "127.0.0.1:PORT" string for a port that is currently
// free, avoiding hardcoded-port collisions when tests run in parallel.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// waitForHTTPReady polls url until it responds with any status code or
// timeout expires. Replaces fixed time.Sleep waits for server startup.
func waitForHTTPReady(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within %v", url, timeout)
}

// mockEngineForHub is a minimal core.Engine implementation for testing
// the hub Start/Stop lifecycle without a real engine.
type mockEngineForHub struct {
	core.Engine
}

func (m *mockEngineForHub) ListDrivers() []core.DriverStatus {
	return []core.DriverStatus{{Name: "d1", Type: "mock", State: core.StateConnected}}
}

func (m *mockEngineForHub) ListTransports() []core.TransportStatus {
	return []core.TransportStatus{{Name: "t1", Type: "mock", State: core.StateConnected}}
}

func (m *mockEngineForHub) LatestValues(driver string) map[string]core.DataPoint {
	return map[string]core.DataPoint{}
}

func (m *mockEngineForHub) StaleThreshold() time.Duration {
	return 0
}

func (m *mockEngineForHub) DeadLetterEntries() []core.DeadLetterEntry {
	return nil
}

func (m *mockEngineForHub) Stats() core.EngineStats {
	return core.EngineStats{Status: core.EngineStatusRunning}
}

func (m *mockEngineForHub) GetRuleStats() []core.RuleStat {
	return []core.RuleStat{}
}

func (m *mockEngineForHub) SetRuleDisabled(index int, disabled bool) error {
	return nil
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty string returns 0", "", 0},
		{"valid 5s", "5s", 5 * time.Second},
		{"valid 200ms", "200ms", 200 * time.Millisecond},
		{"valid 1m30s", "1m30s", 90 * time.Second},
		{"invalid string returns 0", "not-a-duration", 0},
		{"partial invalid returns 0", "5seconds", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDuration(tt.in)
			if got != tt.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestStartStopWithoutAPI(t *testing.T) {
	// When API listen is empty, Start should not create a server.
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			LogLevel: "info",
		},
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock"},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mock"},
		},
	}

	Start(eng, cfg, "config.yaml")

	// No API server should be running — verify by checking that
	// CloseServer returns nil (no server was created).
	if err := route.CloseServer(); err != nil {
		t.Errorf("CloseServer after Start without API should return nil, got %v", err)
	}

	// Stop should also work cleanly.
	Stop()
}

func TestStartStopWithAPI(t *testing.T) {
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			LogLevel: "info",
			API: core.APIConfig{
				Listen: "127.0.0.1:0", // port 0 = OS picks a free port
				Secret: "test-secret",
			},
		},
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock"},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mock"},
		},
	}

	Start(eng, cfg, "config.yaml")

	// CloseServer is safe to call regardless of whether the listener
	// goroutine has started yet (it sets inShutdown), so no sleep needed.
	Stop()

	// After Stop, CloseServer should return nil (server already closed).
	if err := route.CloseServer(); err != nil {
		t.Errorf("CloseServer after Stop should return nil, got %v", err)
	}
}

func TestStartWithAPITimeouts(t *testing.T) {
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			LogLevel: "info",
			API: core.APIConfig{
				Listen:            "127.0.0.1:0",
				Secret:            "test-secret",
				ReadHeaderTimeout: "5s",
				ReadTimeout:       "10s",
				WriteTimeout:      "10s",
				IdleTimeout:       "30s",
			},
		},
	}

	Start(eng, cfg, "config.yaml")
	Stop()
}

func TestStartWithInvalidTimeouts(t *testing.T) {
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			LogLevel: "info",
			API: core.APIConfig{
				Listen:            "127.0.0.1:0",
				Secret:            "test-secret",
				ReadHeaderTimeout: "invalid",
				ReadTimeout:       "also-invalid",
				WriteTimeout:      "",
				IdleTimeout:       "",
			},
		},
	}

	// Should not panic — invalid durations fall back to 0 (then route
	// applies its own defaults).
	Start(eng, cfg, "config.yaml")
	Stop()
}

func TestStopWhenNoServer(t *testing.T) {
	// Ensure CloseServer is clean before calling Stop.
	route.CloseServer()

	// Stop should not panic when no server was started.
	Stop()
}

func TestStartIdempotent(t *testing.T) {
	// Calling Start twice should not panic — the second call replaces
	// the server.
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			API: core.APIConfig{
				Listen: "127.0.0.1:0",
				Secret: "test-secret",
			},
		},
	}

	Start(eng, cfg, "config.yaml")
	Start(eng, cfg, "config.yaml") // should replace, not panic
	Stop()
}

func TestStartWithAllowedOrigins(t *testing.T) {
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			API: core.APIConfig{
				Listen:         "127.0.0.1:0",
				Secret:         "test-secret",
				AllowedOrigins: []string{"http://localhost:3000", "https://dashboard.example.com"},
			},
		},
	}

	Start(eng, cfg, "config.yaml")
	Stop()
}

func TestStartWithRateLimit(t *testing.T) {
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			API: core.APIConfig{
				Listen:          "127.0.0.1:0",
				Secret:          "test-secret",
				RateLimitPerSec: 10,
			},
		},
	}

	Start(eng, cfg, "config.yaml")
	Stop()
}

// TestStartWithAPIAndVerifyHealth checks that the API server actually
// responds to requests after Start.
// TestStartWithMetricsAddr verifies that hub.Start passes the configured
// global.api.metrics-addr through to the route server config: the
// credential-free dedicated /metrics server must actually come up, and
// the authenticated copy on the main API must be suppressed (404).
// Regression guard for the wiring gap where MetricsAddr was parsed but
// never passed to route.Config, silently disabling the whole feature.
func TestStartWithMetricsAddr(t *testing.T) {
	apiAddr := freeAddr(t)
	metricsAddr := freeAddr(t)
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			API: core.APIConfig{
				Listen:      apiAddr,
				Secret:      "test-secret",
				MetricsAddr: metricsAddr,
			},
		},
	}

	Start(eng, cfg, "config.yaml")
	defer Stop()

	// The dedicated server must serve /metrics without any auth header.
	waitForHTTPReady(t, "http://"+metricsAddr+"/metrics", 2*time.Second)
	resp, err := http.Get("http://" + metricsAddr + "/metrics")
	if err != nil {
		t.Fatalf("dedicated metrics server not reachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("dedicated /metrics without auth: expected 200, got %d", resp.StatusCode)
	}

	// The main API must suppress its own /metrics copy (single exposure
	// path) — 404 even for authenticated callers.
	req, _ := http.NewRequest("GET", "http://"+apiAddr+"/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("main API not reachable: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("main API /metrics with dedicated server on: expected 404, got %d", resp2.StatusCode)
	}
}

func TestStartWithAPIAndVerifyHealth(t *testing.T) {
	// Use a dynamic port to avoid collisions with other tests.
	addr := freeAddr(t)
	eng := &mockEngineForHub{}
	cfg := &core.Config{
		Global: core.GlobalConfig{
			API: core.APIConfig{
				Listen: addr,
				Secret: "test-secret",
			},
		},
	}

	Start(eng, cfg, "config.yaml")

	// Poll the health endpoint until the server is ready (replaces fixed sleep).
	waitForHTTPReady(t, "http://"+addr+"/", 2*time.Second)

	// Health endpoint should respond.
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Version endpoint should respond.
	resp2, err := http.Get("http://" + addr + "/version")
	if err != nil {
		t.Fatalf("version check failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp2.StatusCode)
	}

	// Protected endpoint without auth should return 401.
	resp3, err := http.Get("http://" + addr + "/drivers")
	if err != nil {
		t.Fatalf("drivers check failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401 without auth, got %d", resp3.StatusCode)
	}

	// Protected endpoint with auth should return 200.
	req, _ := http.NewRequest("GET", "http://"+addr+"/drivers", http.NoBody)
	req.Header.Set("Authorization", "Bearer test-secret")
	resp4, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authenticated drivers check failed: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Errorf("expected status 200 with auth, got %d", resp4.StatusCode)
	}

	Stop()
}
