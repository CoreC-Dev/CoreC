package route

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

type mockEngine struct {
	core.Engine
	ruleStats   []core.RuleStat
	disabledIdx int
	disabledVal bool
}

func (m *mockEngine) Stats() core.EngineStats {
	return core.EngineStats{
		Status:       core.EngineStatusRunning,
		Uptime:       10 * time.Second,
		Drivers:      2,
		Transports:   1,
		Rules:        3,
		TotalRead:    100,
		TotalPublish: 50,
	}
}

func (m *mockEngine) ListDrivers() []core.DriverStatus {
	return []core.DriverStatus{
		{Name: "plc1", Type: "modbus-tcp", State: core.StateConnected},
	}
}

func (m *mockEngine) ListTransports() []core.TransportStatus {
	return []core.TransportStatus{
		{Name: "mqtt1", Type: "mqtt", State: core.StateConnected},
	}
}

func (m *mockEngine) LatestValues(driver string) map[string]core.DataPoint {
	return map[string]core.DataPoint{
		"temp": {Driver: "plc1", Tag: "temp", Value: 25.5},
	}
}

func (m *mockEngine) StaleThreshold() time.Duration { return 0 }
func (m *mockEngine) DeadLetterEntries() []core.DeadLetterEntry { return nil }

func (m *mockEngine) WriteTag(ctx context.Context, cmd core.WriteCommand) (*core.WriteResult, error) {
	return &core.WriteResult{
		Success: true,
	}, nil
}

func (m *mockEngine) GetRuleStats() []core.RuleStat {
	return m.ruleStats
}

func (m *mockEngine) SetRuleDisabled(index int, disabled bool) error {
	m.disabledIdx = index
	m.disabledVal = disabled
	return nil
}

func TestRoutes(t *testing.T) {
	SetEngine(&mockEngine{})
	GetConfigFunc = func() *core.Config {
		return &core.Config{
			Global: core.GlobalConfig{LogLevel: "info"},
			Drivers: []core.DriverConfig{
				{Name: "plc1", Type: "modbus-tcp"},
				{Name: "plc2", Type: "s7"},
			},
			Transports: []core.TransportConfig{
				{Name: "mqtt1", Type: "mqtt"},
			},
			Rules: []core.RuleConfig{
				{Name: "r1"}, {Name: "r2"}, {Name: "r3"},
			},
		}
	}
	defer func() { GetConfigFunc = nil }()

	secret := "secret-123"
	handler := router(secret, nil, 0)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	client := ts.Client()

	// 1. GET / (no auth needed)
	resp, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / expected 200, got %d", resp.StatusCode)
	}
	var info map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info["name"] != "corec" {
		t.Fatalf("expected name=corec, got %+v", info)
	}
	if info["version"] == "" {
		t.Fatalf("expected non-empty version, got %+v", info)
	}
	if info["status"] != "ok" {
		t.Fatalf("expected status=ok, got %+v", info)
	}
	if info["time"] == "" {
		t.Fatalf("expected non-empty time, got %+v", info)
	}
	if info["uptime"] == "" {
		t.Fatalf("expected non-empty uptime, got %+v", info)
	}

	// 2. GET /configs without auth -> 401
	resp, err = client.Get(ts.URL + "/configs")
	if err != nil {
		t.Fatalf("GET /configs failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. GET /configs with Bearer auth -> 200
	req, _ := http.NewRequest("GET", ts.URL+"/configs", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /configs with auth failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var cfgOverview configOverview
	_ = json.NewDecoder(resp.Body).Decode(&cfgOverview)
	resp.Body.Close()
	if len(cfgOverview.Drivers) != 2 {
		t.Fatalf("expected 2 drivers in config overview, got %+v", cfgOverview)
	}

	// 4. GET /drivers
	req, _ = http.NewRequest("GET", ts.URL+"/drivers", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /drivers failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var driversResp struct {
		Drivers []core.DriverStatus `json:"drivers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&driversResp)
	resp.Body.Close()
	if len(driversResp.Drivers) != 1 || driversResp.Drivers[0].Name != "plc1" {
		t.Fatalf("unexpected drivers: %+v", driversResp)
	}

	// 5. GET /transports
	req, _ = http.NewRequest("GET", ts.URL+"/transports", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /transports failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var transportsResp struct {
		Transports []core.TransportStatus `json:"transports"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&transportsResp)
	resp.Body.Close()
	if len(transportsResp.Transports) != 1 || transportsResp.Transports[0].Name != "mqtt1" {
		t.Fatalf("unexpected transports: %+v", transportsResp)
	}
}

func TestCORS(t *testing.T) {
	me := &mockEngine{}
	SetEngine(me)

	// Only explicitly allowed origins should receive CORS headers (M3 fix).
	handler := router("", []string{"http://localhost:3000"}, 0)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	req, _ := http.NewRequest("OPTIONS", ts.URL+"/drivers", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("OPTIONS failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("expected CORS header http://localhost:3000, got %s", resp.Header.Get("Access-Control-Allow-Origin"))
	}

	// Unallowed origin should NOT receive CORS headers.
	req2, _ := http.NewRequest("OPTIONS", ts.URL+"/drivers", http.NoBody)
	req2.Header.Set("Origin", "http://evil.example.com")
	resp2, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatalf("OPTIONS failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no CORS header for unallowed origin, got %s", resp2.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestGetStats(t *testing.T) {
	me := &mockEngine{}
	SetEngine(me)

	handler := router("", nil, 0)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("GET /stats failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var stats core.EngineStats
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	if stats.Drivers != 2 {
		t.Errorf("expected 2 drivers, got %d", stats.Drivers)
	}
}

func TestGetRulesWithStats(t *testing.T) {
	me := &mockEngine{
		ruleStats: []core.RuleStat{
			{Index: 0, Name: "rule-a", Action: "forward", HitCount: 10, MissCount: 5},
			{Index: 1, Name: "rule-b", Action: "drop", HitCount: 3, MissCount: 7, Disabled: true},
		},
	}
	SetEngine(me)

	handler := router("", nil, 0)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/rules")
	if err != nil {
		t.Fatalf("GET /rules failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Rules []core.RuleStat `json:"rules"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(result.Rules))
	}
	if result.Rules[0].HitCount != 10 {
		t.Errorf("expected hit count 10, got %d", result.Rules[0].HitCount)
	}
	if !result.Rules[1].Disabled {
		t.Error("expected rule-b to be disabled")
	}
}

func TestDisableRule(t *testing.T) {
	me := &mockEngine{}
	SetEngine(me)

	handler := router("", nil, 0)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"index": 1, "disabled": true})
	req, _ := http.NewRequest("PATCH", ts.URL+"/rules/disable", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH /rules/disable failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if me.disabledIdx != 1 || !me.disabledVal {
		t.Errorf("expected index=1 disabled=true, got index=%d disabled=%v", me.disabledIdx, me.disabledVal)
	}
}

func TestAuthWithQueryToken(t *testing.T) {
	me := &mockEngine{}
	SetEngine(me)

	secret := "my-secret"
	handler := router(secret, nil, 0)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Using ?token= query parameter (for WebSocket clients)
	resp, err := ts.Client().Get(ts.URL + "/drivers?token=" + secret)
	if err != nil {
		t.Fatalf("GET /drivers?token= failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with query token, got %d", resp.StatusCode)
	}

	// Wrong token
	resp2, err := ts.Client().Get(ts.URL + "/drivers?token=wrong")
	if err != nil {
		t.Fatalf("GET /drivers?token=wrong failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", resp2.StatusCode)
	}
}
