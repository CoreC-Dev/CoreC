package route

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

type mockEngine struct {
	core.Engine
	ruleStats    []core.RuleStat
	disabledIdx  int
	disabledVal  bool
	disabledName string
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

func (m *mockEngine) StaleThreshold() time.Duration             { return 0 }
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

func (m *mockEngine) SetRuleDisabledByName(name string, disabled bool) error {
	m.disabledName = name
	m.disabledVal = disabled
	return nil
}

func TestRoutes(t *testing.T) { //nolint:gocyclo // comprehensive routing table test; complexity 21.
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
	handler := router(context.Background(), secret, nil, 0, true, false)
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
	handler := router(context.Background(), "", []string{"http://localhost:3000"}, 0, true, false)
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

	handler := router(context.Background(), "", nil, 0, true, false)
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

	handler := router(context.Background(), "", nil, 0, true, false)
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

	handler := router(context.Background(), "", nil, 0, true, false)
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

func TestDisableRuleByName(t *testing.T) {
	me := &mockEngine{}
	SetEngine(me)

	handler := router(context.Background(), "", nil, 0, true, false)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"name": "overtemp-alarm", "disabled": true})
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
	if me.disabledName != "overtemp-alarm" || !me.disabledVal {
		t.Errorf("expected name=overtemp-alarm disabled=true, got name=%q disabled=%v", me.disabledName, me.disabledVal)
	}
	if me.disabledIdx != 0 {
		t.Errorf("index path should not be taken when name is set, got index=%d", me.disabledIdx)
	}
}

func TestDisableRuleNameWinsOverIndex(t *testing.T) {
	me := &mockEngine{}
	SetEngine(me)

	handler := router(context.Background(), "", nil, 0, true, false)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Both fields present: name must win (index may be stale after reload).
	body, _ := json.Marshal(map[string]any{"name": "rule-a", "index": 7, "disabled": false})
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
	if me.disabledName != "rule-a" {
		t.Errorf("expected name addressing to win, got name=%q idx=%d", me.disabledName, me.disabledIdx)
	}
}

func TestAuthWithQueryToken(t *testing.T) {
	me := &mockEngine{}
	SetEngine(me)

	secret := "my-secret"
	handler := router(context.Background(), secret, nil, 0, true, false)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Query-parameter token is only allowed for WebSocket upgrade requests.
	// A regular HTTP GET with ?token= must be rejected (401) to prevent
	// token leakage via browser history, access logs, and Referer headers.
	resp, err := ts.Client().Get(ts.URL + "/drivers?token=" + secret)
	if err != nil {
		t.Fatalf("GET /drivers?token= failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for non-WebSocket query token, got %d", resp.StatusCode)
	}

	// WebSocket upgrade requests may use ?token= (browser WS API cannot
	// set custom Authorization headers).
	req, err := http.NewRequest("GET", ts.URL+"/drivers?token="+secret, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	resp2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("WebSocket upgrade request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with WebSocket upgrade query token, got %d", resp2.StatusCode)
	}

	// Wrong token on WebSocket upgrade
	req2, err := http.NewRequest("GET", ts.URL+"/drivers?token=wrong", http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req2.Header.Set("Upgrade", "websocket")
	req2.Header.Set("Connection", "Upgrade")
	resp3, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", resp3.StatusCode)
	}
}

// TestPprofDisabled verifies that when pprofEnabled is false, the pprof
// endpoints are not registered on the main router.
func TestPprofDisabled(t *testing.T) {
	handler := router(context.Background(), "", nil, 0, false, false) // pprofEnabled = false
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/ failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 when pprof disabled, got %d", resp.StatusCode)
	}
}

// TestPprofEnabled verifies that when pprofEnabled is true, the pprof
// endpoints are registered on the main router.
func TestPprofEnabled(t *testing.T) {
	handler := router(context.Background(), "", nil, 0, true, false) // pprofEnabled = true
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/ failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Error("expected pprof to be available when enabled, got 404")
	}
}

// TestGetFailedWritesSource verifies the ?source= parameter of
// GET /write/failed (audit RES-1/TRA-4): the default serves the engine
// dead-letter queue, source=ingress serves commands dropped at transport
// ingress, and unknown values are rejected with 400 instead of silently
// falling back to the wrong queue.
func TestGetFailedWritesSource(t *testing.T) {
	engineEntry := core.DeadLetterEntry{
		Command:  core.WriteCommand{Driver: "plc", Tag: "engine-tag"},
		Error:    "write failed after retries",
		Attempts: 4,
	}
	ingressEntry := core.DeadLetterEntry{
		Command:  core.WriteCommand{Driver: "plc", Tag: "ingress-tag"},
		Error:    "command channel full at ingress",
		Attempts: 1,
	}
	eng := &mockEngineV2{
		deadLetters:        []core.DeadLetterEntry{engineEntry},
		ingressDeadLetters: []core.DeadLetterEntry{ingressEntry},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	// Default: engine queue.
	resp := authedGet(t, ts, "/write/failed", "")
	defer resp.Body.Close()
	var body struct {
		FailedWrites []core.DeadLetterEntry `json:"failed_writes"`
		Count        int                    `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode default response: %v", err)
	}
	if body.Count != 1 || body.FailedWrites[0].Command.Tag != "engine-tag" {
		t.Errorf("default response = %+v, want the engine queue entry", body)
	}

	// source=engine: explicit same as default.
	resp2 := authedGet(t, ts, "/write/failed?source=engine", "")
	defer resp2.Body.Close()
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatalf("decode engine response: %v", err)
	}
	if body.Count != 1 || body.FailedWrites[0].Command.Tag != "engine-tag" {
		t.Errorf("source=engine response = %+v, want the engine queue entry", body)
	}

	// source=ingress: the transport-side drops.
	resp3 := authedGet(t, ts, "/write/failed?source=ingress", "")
	defer resp3.Body.Close()
	if err := json.NewDecoder(resp3.Body).Decode(&body); err != nil {
		t.Fatalf("decode ingress response: %v", err)
	}
	if body.Count != 1 || body.FailedWrites[0].Command.Tag != "ingress-tag" {
		t.Errorf("source=ingress response = %+v, want the ingress drop entry", body)
	}

	// Unknown source: 400, not a silent fallback.
	resp4 := authedGet(t, ts, "/write/failed?source=bogus", "")
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusBadRequest {
		t.Errorf("source=bogus status = %d, want 400", resp4.StatusCode)
	}
}

// TestUpdateConfigsErrorClassification verifies PUT /configs answers
// config parse/validation failures with a 400 carrying the actual
// reason (client-fixable), while apply-stage failures keep the masked
// 500 (audit findings CFG-3/HUB-2). The route never imports the
// executor package, so the client-fixable marker is duck-typed.
type fixableConfigErr struct{ msg string }

func (e fixableConfigErr) Error() string       { return e.msg }
func (e fixableConfigErr) ClientFixable() bool { return true }

func TestUpdateConfigsErrorClassification(t *testing.T) {
	t.Run("validation failure answers 400 with the reason", func(t *testing.T) {
		ReloadFunc = func(path, payload, remote string) error {
			return fixableConfigErr{msg: "config validation failed: duplicate driver name: plc1"}
		}
		defer func() { ReloadFunc = nil }()

		ts := newTestServer("", &mockEngineV2{})
		defer ts.Close()

		resp := putConfigs(t, ts, `{"payload":"drivers: []"}`)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		var body map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !strings.Contains(body["error"], "duplicate driver name: plc1") {
			t.Errorf("error = %q, want the validation reason, not a masked message", body["error"])
		}
	})

	t.Run("apply failure stays a masked 500", func(t *testing.T) {
		ReloadFunc = func(path, payload, remote string) error {
			return errors.New("failed to add transport mqtt1: connection refused to 10.0.0.5:1883")
		}
		defer func() { ReloadFunc = nil }()

		ts := newTestServer("", &mockEngineV2{})
		defer ts.Close()

		resp := putConfigs(t, ts, `{"payload":"transports: []"}`)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", resp.StatusCode)
		}
		var body map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body["error"] != "internal server error" {
			t.Errorf("error = %q, want masked internal server error (apply failures can leak internals)", body["error"])
		}
	})
}

// putConfigs issues an authenticated PUT /configs with the given JSON
// body and returns the raw response.
func putConfigs(t *testing.T, ts *httptest.Server, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("PUT", ts.URL+"/configs", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT /configs failed: %v", err)
	}
	return resp
}

// TestDriverPauseResumeEndpoints verifies the pause/resume API family
// (audit RES-4, roadmap #21): 204 on pause and resume, 404 for unknown
// drivers, and GET /drivers/paused listing the paused set (also proving
// the static /drivers/paused route is not swallowed by /drivers/{name}).
type mockEnginePausable struct {
	mockEngineV2
	pausedSet map[string]bool
}

func (m *mockEnginePausable) PauseDriver(name string) error {
	m.pausedSet[name] = true
	return nil
}
func (m *mockEnginePausable) ResumeDriver(name string) error {
	delete(m.pausedSet, name)
	return nil
}
func (m *mockEnginePausable) PausedDrivers() []string {
	names := make([]string, 0, len(m.pausedSet))
	for n := range m.pausedSet {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func TestDriverPauseResumeEndpoints(t *testing.T) {
	eng := &mockEnginePausable{
		pausedSet: make(map[string]bool),
		drivers: []core.DriverStatus{
			{Name: "plc1", Type: "modbus-tcp"},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	do := func(method, path string) *http.Response {
		req, err := http.NewRequest(method, ts.URL+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	// Pause an existing driver.
	if got := do(http.MethodPost, "/drivers/plc1/pause").StatusCode; got != http.StatusNoContent {
		t.Errorf("pause status = %d, want 204", got)
	}
	// The paused list reflects it — and the static route wins over
	// /drivers/{name}.
	resp := authedGet(t, ts, "/drivers/paused", "")
	defer resp.Body.Close()
	var body struct {
		Paused []string `json:"paused"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode paused list: %v", err)
	}
	if len(body.Paused) != 1 || body.Paused[0] != "plc1" {
		t.Errorf("paused = %v, want [plc1]", body.Paused)
	}

	// Resume clears it.
	if got := do(http.MethodPost, "/drivers/plc1/resume").StatusCode; got != http.StatusNoContent {
		t.Errorf("resume status = %d, want 204", got)
	}
	resp2 := authedGet(t, ts, "/drivers/paused", "")
	defer resp2.Body.Close()
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatalf("decode paused list: %v", err)
	}
	if len(body.Paused) != 0 {
		t.Errorf("paused after resume = %v, want empty", body.Paused)
	}

	// Unknown drivers 404.
	if got := do(http.MethodPost, "/drivers/nope/pause").StatusCode; got != http.StatusNotFound {
		t.Errorf("pause unknown status = %d, want 404", got)
	}
	if got := do(http.MethodPost, "/drivers/nope/resume").StatusCode; got != http.StatusNotFound {
		t.Errorf("resume unknown status = %d, want 404", got)
	}
}

// TestGetConfigAudit verifies the config change audit trail (audit
// HUB-4 / roadmap #22): reloads and patches append bounded, newest-first
// entries with the caller's address, exposed via GET /configs/audit.
func TestGetConfigAudit(t *testing.T) {
	prevList := ConfigAuditFunc
	ConfigAuditFunc = func() []core.ConfigAuditEntry {
		return []core.ConfigAuditEntry{
			{At: time.Now(), Action: "patch", Remote: "192.168.1.10:53412", Keys: []string{"log-level"}},
			{At: time.Now().Add(-time.Minute), Action: "reload", Remote: "192.168.1.10:53400", Keys: []string{"payload"}},
		}
	}
	defer func() { ConfigAuditFunc = prevList }()

	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/configs/audit", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Changes []core.ConfigAuditEntry `json:"changes"`
		Count   int                     `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 2 || len(body.Changes) != 2 {
		t.Fatalf("count = %d, changes = %d, want 2/2", body.Count, len(body.Changes))
	}
	// Newest first: the patch (now) before the reload (a minute ago).
	if body.Changes[0].Action != "patch" || body.Changes[1].Action != "reload" {
		t.Errorf("order = %v then %v, want patch (newest) first", body.Changes[0].Action, body.Changes[1].Action)
	}
	if body.Changes[0].Remote == "" || body.Changes[0].Keys[0] != "log-level" {
		t.Errorf("patch entry = %+v, want remote + log-level key", body.Changes[0])
	}
}
