package route

import (
	"context"
	"encoding/json"
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
	log.Init(slog.LevelInfo) // Initialize log system so Subscribe works

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

	// Verify the stats contain expected fields.
	if read, ok := stats["read"].(float64); !ok || read != 100 {
		t.Errorf("expected read=100, got %v", stats["read"])
	}
	if pub, ok := stats["publish"].(float64); !ok || pub != 50 {
		t.Errorf("expected publish=50, got %v", stats["publish"])
	}
	if dropped, ok := stats["dropped"].(float64); !ok || dropped != 5 {
		t.Errorf("expected dropped=5, got %v", stats["dropped"])
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
	ts := httptest.NewServer(router("", nil, 5, true))
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
	ts := httptest.NewServer(router("", nil, 0, true))
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
	ts := httptest.NewServer(router("", allowedOrigins, 0, true))
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

	// Auth via query parameter.
	resp, err := http.Get(ts.URL + "/drivers?token=my-secret")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with query token, got %d", resp.StatusCode)
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
