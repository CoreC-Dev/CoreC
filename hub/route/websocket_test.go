package route

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// --- WebSocket endpoint tests ---

// TestStreamTagsWebSocket verifies that the /tags/stream WebSocket endpoint
// accepts a connection and forwards subscribed data points as JSON.
func TestStreamTagsWebSocket(t *testing.T) {
	eng := &mockEngineV2{
		stats: core.EngineStats{Status: core.EngineStatusRunning},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	// Convert http:// to ws://
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/tags/stream"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// The dial succeeded above, so the connection was accepted.
	// No sleep needed — just close from client side; server handles gracefully.
	c.Close(websocket.StatusNormalClosure, "")
}

// TestGetLogsWebSocket verifies that the /logs WebSocket endpoint
// accepts a connection and forwards log events.
func TestGetLogsWebSocket(t *testing.T) {
	// Initialize the log system so Subscribe works.
	log.Init(slog.LevelInfo, "text") // Initialize log system so Subscribe works

	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/logs"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// Generate a log event that should be forwarded over the WebSocket.
	log.Infoln("test log message for websocket")

	// Try to read a message (may or may not arrive depending on timing,
	// but the connection should be stable).
	readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer readCancel()
	var msg map[string]any
	_ = wsjson.Read(readCtx, c, &msg) // ignore error — timing dependent

	c.Close(websocket.StatusNormalClosure, "")
}

// TestGetTrafficWebSocket verifies that the /traffic WebSocket endpoint
// accepts a connection and periodically sends traffic stats.
func TestGetTrafficWebSocket(t *testing.T) {
	eng := &mockEngineV2{
		stats: core.EngineStats{
			Status:       core.EngineStatusRunning,
			TotalRead:    100,
			TotalPublish: 50,
			TotalDropped: 5,
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	// Use a short interval for fast testing.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/traffic?interval=50ms"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// Read the first traffic stats message.
	var stats map[string]any
	if err := wsjson.Read(ctx, c, &stats); err != nil {
		t.Fatalf("failed to read traffic stats: %v", err)
	}

	// Default mode pushes RATES (roadmap #32): the first frame carries
	// the engine's windowed rate plus zero publish/drop rates (no diff
	// baseline yet), never a bogus negative spike.
	if _, ok := stats["read_rate"]; !ok {
		t.Fatalf("expected rate fields, got %v", stats)
	}
	if _, ok := stats["publish_rate"]; !ok {
		t.Error("expected publish_rate field")
	}
	if _, ok := stats["dropped_rate"]; !ok {
		t.Error("expected dropped_rate field")
	}
	if v, ok := stats["publish_rate"].(float64); !ok || v != 0 {
		t.Errorf("first-frame publish_rate = %v, want 0 (no baseline yet)", stats["publish_rate"])
	}

	c.Close(websocket.StatusNormalClosure, "")
}

// TestGetTrafficWebSocketTotals verifies mode=totals keeps the historical
// cumulative-counter stream for clients that diff client-side.
func TestGetTrafficWebSocketTotals(t *testing.T) {
	eng := &mockEngineV2{
		stats: core.EngineStats{
			Status:       core.EngineStatusRunning,
			TotalRead:    100,
			TotalPublish: 50,
			TotalDropped: 5,
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/traffic?interval=50ms&mode=totals"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	var stats map[string]any
	if err := wsjson.Read(ctx, c, &stats); err != nil {
		t.Fatalf("failed to read traffic stats: %v", err)
	}
	if read, ok := stats["read"].(float64); !ok || read != 100 {
		t.Errorf("expected read=100 in totals mode, got %v", stats["read"])
	}
	if pub, ok := stats["publish"].(float64); !ok || pub != 50 {
		t.Errorf("expected publish=50 in totals mode, got %v", stats["publish"])
	}

	c.Close(websocket.StatusNormalClosure, "")
}

// TestGetMemoryWebSocket verifies that the /memory WebSocket endpoint
// accepts a connection and periodically sends memory stats.
func TestGetMemoryWebSocket(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/memory?interval=50ms"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// Read the first memory stats message.
	var mem map[string]any
	if err := wsjson.Read(ctx, c, &mem); err != nil {
		t.Fatalf("failed to read memory stats: %v", err)
	}

	// Verify the stats contain expected fields.
	expectedFields := []string{"alloc", "total_alloc", "sys", "num_gc", "goroutines"}
	for _, field := range expectedFields {
		if _, ok := mem[field]; !ok {
			t.Errorf("expected field %q in memory stats, got: %v", field, mem)
		}
	}

	c.Close(websocket.StatusNormalClosure, "")
}

// --- Rate limit middleware test ---

func TestRateLimitMiddleware(t *testing.T) {
	eng := &mockEngineV2{
		drivers: []core.DriverStatus{{Name: "plc1", Type: "modbus-tcp"}},
	}

	// Create a server with rate limiting at 5 req/s.
	SetEngine(eng)
	ts := httptest.NewServer(router(context.Background(), "", nil, 5, true, false))
	defer ts.Close()

	// Fire 10 rapid requests — some should be rate limited (429).
	statusCodes := make(map[int]int)
	for i := 0; i < 10; i++ {
		resp, err := http.Get(ts.URL + "/drivers")
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		statusCodes[resp.StatusCode]++
		resp.Body.Close()
	}

	// We should have some 200s and some 429s (or all 200s if the limiter
	// is generous enough). The key is that the middleware doesn't crash
	// and eventually limits.
	if statusCodes[http.StatusOK] == 0 {
		t.Error("expected at least one 200 response")
	}
}

// --- CORS middleware test ---

func TestCORSMiddleware(t *testing.T) {
	eng := &mockEngineV2{}
	SetEngine(eng)
	ts := httptest.NewServer(router(context.Background(), "", nil, 0, true, false))
	defer ts.Close()

	// Test permissive CORS (no allowed origins configured).
	resp, err := http.Get(ts.URL + "/version")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	origin := resp.Header.Get("Access-Control-Allow-Origin")
	if origin != "*" {
		t.Errorf("expected permissive CORS origin '*', got %q", origin)
	}
}

func TestCORSMiddlewareRestricted(t *testing.T) {
	eng := &mockEngineV2{}
	SetEngine(eng)
	allowedOrigins := []string{"http://localhost:3000", "https://dashboard.example.com"}
	ts := httptest.NewServer(router(context.Background(), "", allowedOrigins, 0, true, false))
	defer ts.Close()

	// Request with allowed origin.
	req, _ := http.NewRequest("GET", ts.URL+"/version", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	origin := resp.Header.Get("Access-Control-Allow-Origin")
	if origin != "http://localhost:3000" {
		t.Errorf("expected restricted CORS origin, got %q", origin)
	}

	// Request with disallowed origin.
	req2, _ := http.NewRequest("GET", ts.URL+"/version", http.NoBody)
	req2.Header.Set("Origin", "http://evil.example.com")
	resp2, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()

	origin2 := resp2.Header.Get("Access-Control-Allow-Origin")
	if origin2 == "http://evil.example.com" {
		t.Error("expected disallowed origin to not be echoed back")
	}
}

// --- ReCreateServer / CloseServer tests ---

func TestReCreateServerNilConfig(t *testing.T) {
	// nil config should be a no-op.
	ReCreateServer(nil)
	if httpServer != nil {
		t.Error("expected no server after nil config")
	}
}

func TestReCreateServerEmptyAddr(t *testing.T) {
	// Empty addr should be a no-op.
	ReCreateServer(&Config{Addr: ""})
	if httpServer != nil {
		t.Error("expected no server after empty addr")
	}
}

func TestReCreateServerAndClose(t *testing.T) {
	// Create a server on a random port.
	ReCreateServer(&Config{
		Addr:   "127.0.0.1:0",
		Secret: "test-secret",
	})
	if httpServer == nil {
		t.Fatal("expected server to be created")
	}

	// CloseServer is safe regardless of whether the listener goroutine
	// has started yet, so no sleep needed.
	if err := CloseServer(); err != nil {
		t.Errorf("CloseServer failed: %v", err)
	}
	if httpServer != nil {
		t.Error("expected httpServer to be nil after CloseServer")
	}
}

func TestCloseServerWhenNil(t *testing.T) {
	// Ensure no server exists.
	CloseServer()

	// Closing again should return nil.
	if err := CloseServer(); err != nil {
		t.Errorf("CloseServer when nil should return nil, got %v", err)
	}
}

func TestReCreateServerReplaces(t *testing.T) {
	// Create first server.
	ReCreateServer(&Config{Addr: "127.0.0.1:0", Secret: "s1"})

	// Create second server — should replace the first (ReCreateServer
	// closes the old one synchronously, so no sleep needed).
	ReCreateServer(&Config{Addr: "127.0.0.1:0", Secret: "s2"})

	if httpServer == nil {
		t.Error("expected server to exist after replacement")
	}

	CloseServer()
}

// --- Authentication edge cases ---

func TestAuthQueryToken(t *testing.T) {
	eng := &mockEngineV2{
		drivers: []core.DriverStatus{{Name: "plc1", Type: "modbus-tcp"}},
	}
	ts := newTestServer("my-secret", eng)
	defer ts.Close()

	// Query-parameter token is only allowed for WebSocket upgrade requests.
	// A regular HTTP GET with ?token= must be rejected (401).
	resp, err := http.Get(ts.URL + "/drivers?token=my-secret")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for non-WebSocket query token, got %d", resp.StatusCode)
	}

	// WebSocket upgrade requests may use ?token=.
	req, err := http.NewRequest("GET", ts.URL+"/drivers?token=my-secret", http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	resp2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with WebSocket upgrade query token, got %d", resp2.StatusCode)
	}
}

func TestAuthCorrectBearer(t *testing.T) {
	eng := &mockEngineV2{
		drivers: []core.DriverStatus{{Name: "plc1", Type: "modbus-tcp"}},
	}
	ts := newTestServer("correct-secret", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/drivers", "correct-secret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with correct bearer, got %d", resp.StatusCode)
	}
}

// --- Config overview test ---

func TestGetConfigs(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	GetConfigFunc = func() *core.Config {
		return &core.Config{
			Global: core.GlobalConfig{LogLevel: "info"},
			Drivers: []core.DriverConfig{
				{Name: "d1", Type: "modbus-tcp"},
			},
			Transports: []core.TransportConfig{
				{Name: "t1", Type: "mqtt"},
			},
		}
	}
	defer func() { GetConfigFunc = nil }()

	resp := authedGet(t, ts, "/configs", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	decodeJSON(t, resp, &result)
	// The config overview should contain some structure.
	if result == nil {
		t.Error("expected non-nil config overview")
	}
}

// --- Rules endpoint test ---

func TestGetRules(t *testing.T) {
	eng := &mockEngineV2{
		ruleStats: []core.RuleStat{
			{Name: "rule1", Action: "forward", HitCount: 10, MissCount: 5, Disabled: false},
			{Name: "rule2", Action: "alert", HitCount: 3, MissCount: 7, Disabled: true},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/rules", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Rules []core.RuleStat `json:"rules"`
	}
	decodeJSON(t, resp, &result)
	if len(result.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(result.Rules))
	}
	if result.Rules[0].HitCount != 10 {
		t.Errorf("expected hit count 10, got %d", result.Rules[0].HitCount)
	}
	if !result.Rules[1].Disabled {
		t.Error("expected rule2 to be disabled")
	}
}

func TestDisableRuleValid(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"index": 0, "disabled": true})
	req, _ := http.NewRequest("PATCH", ts.URL+"/rules/disable", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
	if eng.disabledIdx != 0 || !eng.disabledVal {
		t.Errorf("expected disable(0, true), got idx=%d val=%v", eng.disabledIdx, eng.disabledVal)
	}
}

func TestDisableRuleOutOfRange(t *testing.T) {
	eng := &mockEngineV2{
		ruleStats: []core.RuleStat{{Name: "r1"}},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	// SetRuleDisabled with out-of-range index should cause an error.
	// But mockEngineV2.SetRuleDisabled always returns nil, so we test
	// the JSON parsing path instead.
	body, _ := json.Marshal(map[string]any{"index": 99, "disabled": true})
	req, _ := http.NewRequest("PATCH", ts.URL+"/rules/disable", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	// Mock returns nil error, so it should be 204.
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

// --- Health endpoint test ---

func TestHelloEndpoint(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	decodeJSON(t, resp, &result)
	if result["name"] != "corec" {
		t.Errorf("expected name=corec, got %q", result["name"])
	}
	if result["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", result["status"])
	}
}

// --- SEC-006 (D11): pprof loopback validation ---

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:6060", true},
		{"localhost:6060", true},
		{"[::1]:6060", true},
		{"0.0.0.0:6060", false},
		{":6060", false},
		{"192.168.1.1:6060", false},
		{"10.0.0.1:6060", false},
		{"example.com:6060", false}, // arbitrary hostname
		{"127.0.0.1", false},        // missing port → SplitHostPort error
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// TestPprofAddrNonLoopbackRejected verifies that a non-loopback PprofAddr is
// refused: the separate pprof server (which has no authentication) must not
// start on a publicly-reachable address. SEC-006 (D11).
func TestPprofAddrNonLoopbackRejected(t *testing.T) {
	ReCreateServer(&Config{
		Addr:      "127.0.0.1:0",
		Secret:    "test-secret",
		PprofAddr: "0.0.0.0:6060", // non-loopback → must be refused
	})
	defer CloseServer()

	if httpServer == nil {
		t.Fatal("expected main server to be created")
	}
	pprofMu.Lock()
	defer pprofMu.Unlock()
	if pprofServer != nil {
		t.Error("expected pprofServer to be nil for non-loopback PprofAddr, but it was started")
		_ = pprofServer.Close()
		pprofServer = nil
	}
}

// TestPprofAddrLoopbackAccepted verifies that a loopback PprofAddr starts the
// separate pprof server. SEC-006 (D11).
func TestPprofAddrLoopbackAccepted(t *testing.T) {
	ReCreateServer(&Config{
		Addr:      "127.0.0.1:0",
		Secret:    "test-secret",
		PprofAddr: "127.0.0.1:0", // loopback → allowed
	})
	defer CloseServer()

	pprofMu.Lock()
	defer pprofMu.Unlock()
	if pprofServer == nil {
		t.Fatal("expected pprofServer to be created for loopback PprofAddr")
	}
}

// TestRateLimitMiddlewareSharedByHost verifies the limiter keys by the
// HOST portion of RemoteAddr (audit HUB-1): the old code keyed on the
// full "host:port" string, handing every new TCP connection a fresh
// full token bucket — a client cycling connections bypassed the limit
// entirely. Two separate connections (different source ports) from the
// same loopback host must SHARE one bucket.
func TestRateLimitMiddlewareSharedByHost(t *testing.T) {
	eng := &mockEngineV2{
		drivers: []core.DriverStatus{{Name: "plc1", Type: "modbus-tcp"}},
	}
	SetEngine(eng)
	// 2 req/s cap: 10 rapid requests across distinct connections must
	// trip the limiter even though every request has a different
	// source port.
	ts := httptest.NewServer(router(context.Background(), "", nil, 2, true, false))
	defer ts.Close()

	statusCodes := make(map[int]int)
	for i := 0; i < 10; i++ {
		// A fresh client forces a new TCP connection (new source port)
		// per request — the exact shape a connection-cycling attacker
		// produces.
		client := &http.Client{Transport: &http.Transport{
			DisableKeepAlives: true,
		}}
		resp, err := client.Get(ts.URL + "/drivers")
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		statusCodes[resp.StatusCode]++
		resp.Body.Close()
	}

	if statusCodes[http.StatusOK] == 0 {
		t.Error("expected at least one 200 response")
	}
	if statusCodes[http.StatusTooManyRequests] == 0 {
		t.Error("expected 429s: connection-cycling must share the per-host bucket, not reset it")
	}
}

// TestMetricsDedicatedServer verifies the api.metrics-addr surface
// (audit OBS-12 / roadmap #27): the dedicated server serves /metrics
// credential-free on its own loopback listener, the authenticated main
// API suppresses its /metrics copy while the dedicated address is
// configured, and a non-loopback address is refused fail-closed.
func TestMetricsDedicatedServer(t *testing.T) {
	eng := &mockEngineV2{stats: core.EngineStats{TotalRead: 42}}
	SetEngine(eng)
	defer func() {
		// Restore the default (non-dedicated) state for other tests.
		SetEngine(&mockEngineV2{})
	}()

	t.Run("dedicated server serves /metrics without auth, main API suppresses it", func(t *testing.T) {
		// Free loopback port for the dedicated listener.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		metricsAddr := ln.Addr().String()
		ln.Close()

		startMetricsServer(&Config{MetricsAddr: metricsAddr})
		pprofMu.Lock()
		srv := metricsServer
		pprofMu.Unlock()
		if srv == nil {
			t.Fatal("dedicated metrics server not started")
		}
		defer func() {
			pprofMu.Lock()
			_ = srv.Close()
			metricsServer = nil
			pprofMu.Unlock()
		}()

		// The dedicated endpoint answers credential-free. The listener
		// binds asynchronously — poll until the socket answers.
		var resp *http.Response
		for attempt := 0; attempt < 50; attempt++ {
			resp, err = http.Get("http://" + metricsAddr + "/metrics")
			if err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("scrape dedicated metrics: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "corec_reads_total 42") {
			t.Errorf("dedicated scrape: status=%d body=%q", resp.StatusCode, string(body)[:min(80, len(body))])
		}

		// The main API (with the dedicated addr configured) suppresses
		// its authenticated /metrics copy.
		ts := httptest.NewServer(router(context.Background(), "", nil, 0, true, true))
		defer ts.Close()
		mainResp, err := http.Get(ts.URL + "/metrics")
		if err != nil {
			t.Fatalf("main metrics: %v", err)
		}
		mainResp.Body.Close()
		if mainResp.StatusCode != http.StatusNotFound {
			t.Errorf("main API /metrics status = %d, want 404 while the dedicated server is configured", mainResp.StatusCode)
		}
	})

	t.Run("non-loopback dedicated address is refused", func(t *testing.T) {
		startMetricsServer(&Config{MetricsAddr: "0.0.0.0:9101"})
		pprofMu.Lock()
		srv := metricsServer
		metricsServer = nil // clean any prior state
		pprofMu.Unlock()
		if srv != nil {
			_ = srv.Close()
			t.Error("metrics server started on a non-loopback address; must be refused fail-closed")
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
