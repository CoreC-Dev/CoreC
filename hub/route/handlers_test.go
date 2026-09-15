package route

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// --- enhanced mock engine ---

type mockEngineV2 struct {
	core.Engine
	stats        core.EngineStats
	drivers      []core.DriverStatus
	transports   []core.TransportStatus
	tagsByDriver map[string]map[string]core.DataPoint
	writeErr     error
	writeResult  core.WriteResult
	ruleStats    []core.RuleStat
	disabledIdx  int
	disabledVal  bool
}

func (m *mockEngineV2) Stats() core.EngineStats { return m.stats }

func (m *mockEngineV2) ListDrivers() []core.DriverStatus { return m.drivers }

func (m *mockEngineV2) ListTransports() []core.TransportStatus { return m.transports }

func (m *mockEngineV2) LatestValues(driver string) map[string]core.DataPoint {
	if driver == "" {
		// Aggregate all drivers
		all := make(map[string]core.DataPoint)
		for _, tags := range m.tagsByDriver {
			for k := range tags {
				all[k] = tags[k]
			}
		}
		return all
	}
	return m.tagsByDriver[driver]
}

func (m *mockEngineV2) StaleThreshold() time.Duration             { return 0 }
func (m *mockEngineV2) DeadLetterEntries() []core.DeadLetterEntry { return nil }

func (m *mockEngineV2) WriteTag(ctx context.Context, cmd core.WriteCommand) (*core.WriteResult, error) {
	return &m.writeResult, m.writeErr
}

func (m *mockEngineV2) GetRuleStats() []core.RuleStat { return m.ruleStats }

func (m *mockEngineV2) SetRuleDisabled(index int, disabled bool) error {
	m.disabledIdx = index
	m.disabledVal = disabled
	return nil
}

func newTestServer(secret string, eng core.Engine) *httptest.Server {
	SetEngine(eng)
	return httptest.NewServer(router(secret, nil, 0, true))
}

func authedGet(t *testing.T, ts *httptest.Server, path, secret string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, http.NoBody)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
}

// --- tests ---

func TestGetVersion(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	resp := authedGet(t, ts, "/version", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var v map[string]string
	decodeJSON(t, resp, &v)
	if v["version"] != "dev" {
		t.Errorf("expected version=dev, got %q", v["version"])
	}
}

func TestGetDriverByName(t *testing.T) {
	eng := &mockEngineV2{
		drivers: []core.DriverStatus{
			{Name: "plc1", Type: "modbus-tcp", State: core.StateConnected},
			{Name: "plc2", Type: "s7", State: core.StateDisconnected},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	// Found
	resp := authedGet(t, ts, "/drivers/plc1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var d core.DriverStatus
	decodeJSON(t, resp, &d)
	if d.Name != "plc1" || d.Type != "modbus-tcp" {
		t.Errorf("unexpected driver: %+v", d)
	}

	// Not found
	resp2 := authedGet(t, ts, "/drivers/nonexistent", "")
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp2.StatusCode)
	}
	var errResp map[string]string
	decodeJSON(t, resp2, &errResp)
	if errResp["error"] != "driver not found" {
		t.Errorf("expected 'driver not found', got %q", errResp["error"])
	}
}

func TestGetDriverTags(t *testing.T) {
	eng := &mockEngineV2{
		tagsByDriver: map[string]map[string]core.DataPoint{
			"plc1": {
				"temp":  {Driver: "plc1", Tag: "temp", Value: 42.5},
				"press": {Driver: "plc1", Tag: "press", Value: 101.3},
			},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/drivers/plc1/tags", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result struct {
		Tags map[string]core.DataPoint `json:"tags"`
	}
	decodeJSON(t, resp, &result)
	if len(result.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(result.Tags))
	}
}

func TestGetTransportByName(t *testing.T) {
	eng := &mockEngineV2{
		transports: []core.TransportStatus{
			{Name: "mqtt1", Type: "mqtt", State: core.StateConnected},
			{Name: "http1", Type: "http", State: core.StateError},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	// Found
	resp := authedGet(t, ts, "/transports/mqtt1", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var tr core.TransportStatus
	decodeJSON(t, resp, &tr)
	if tr.Name != "mqtt1" {
		t.Errorf("unexpected transport: %+v", tr)
	}

	// Not found
	resp2 := authedGet(t, ts, "/transports/nope", "")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp2.StatusCode)
	}
}

func TestGetAllTags(t *testing.T) {
	eng := &mockEngineV2{
		tagsByDriver: map[string]map[string]core.DataPoint{
			"plc1": {"temp": {Driver: "plc1", Tag: "temp", Value: 25.0}},
			"plc2": {"humidity": {Driver: "plc2", Tag: "humidity", Value: 60.0}},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/tags", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result struct {
		Tags map[string]core.DataPoint `json:"tags"`
	}
	decodeJSON(t, resp, &result)
	if len(result.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(result.Tags))
	}
}

func TestWriteTag(t *testing.T) {
	eng := &mockEngineV2{
		writeResult: core.WriteResult{Success: true},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	body, _ := json.Marshal(core.WriteCommand{
		Driver: "plc1",
		Tag:    "temp",
		Value:  50,
	})
	req, _ := http.NewRequest("POST", ts.URL+"/write", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /write failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result core.WriteResult
	decodeJSON(t, resp, &result)
	if !result.Success {
		t.Error("expected success=true")
	}
}

func TestWriteTagError(t *testing.T) {
	eng := &mockEngineV2{
		writeErr: errors.New("driver offline"),
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	body, _ := json.Marshal(core.WriteCommand{Driver: "plc1", Tag: "temp", Value: 50})
	req, _ := http.NewRequest("POST", ts.URL+"/write", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /write failed: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	var errResp map[string]string
	decodeJSON(t, resp, &errResp)
	if errResp["error"] != "internal server error" {
		t.Errorf("expected 'internal server error', got %q", errResp["error"])
	}
}

func TestWriteTagBadJSON(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/write", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /write failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUpdateConfigs(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	called := false
	ReloadFunc = func(path, payload string) error {
		called = true
		if path != "newconfig.yaml" {
			t.Errorf("expected path=newconfig.yaml, got %q", path)
		}
		return nil
	}
	defer func() { ReloadFunc = nil }()

	body, _ := json.Marshal(map[string]string{"path": "newconfig.yaml"})
	req, _ := http.NewRequest("PUT", ts.URL+"/configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT /configs failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if !called {
		t.Error("expected ReloadFunc to be called")
	}
}

func TestUpdateConfigsError(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	ReloadFunc = func(path, payload string) error {
		return errors.New("config parse error")
	}
	defer func() { ReloadFunc = nil }()

	body, _ := json.Marshal(map[string]string{"path": "bad.yaml"})
	req, _ := http.NewRequest("PUT", ts.URL+"/configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT /configs failed: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPatchConfigs(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	var received map[string]any
	PatchFunc = func(patch map[string]any) error {
		received = patch
		return nil
	}
	defer func() { PatchFunc = nil }()

	body, _ := json.Marshal(map[string]string{"log-level": "debug"})
	req, _ := http.NewRequest("PATCH", ts.URL+"/configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH /configs failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if received["log-level"] != "debug" {
		t.Errorf("expected log-level=debug, got %v", received["log-level"])
	}
}

func TestStatsWithDropped(t *testing.T) {
	eng := &mockEngineV2{
		stats: core.EngineStats{
			Status:       core.EngineStatusRunning,
			TotalRead:    1000,
			TotalPublish: 800,
			TotalDropped: 42,
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/stats", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var stats core.EngineStats
	decodeJSON(t, resp, &stats)
	if stats.TotalDropped != 42 {
		t.Errorf("expected TotalDropped=42, got %d", stats.TotalDropped)
	}
}

func TestAuthWrongBearer(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("correct-secret", eng)
	defer ts.Close()

	// Wrong bearer token
	resp := authedGet(t, ts, "/drivers", "wrong-secret")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Empty Authorization header with no query token
	req, _ := http.NewRequest("GET", ts.URL+"/drivers", http.NoBody)
	req.Header.Set("Authorization", "")
	resp2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for empty auth, got %d", resp2.StatusCode)
	}
}

func TestAuthEmptySecretAllowsAll(t *testing.T) {
	eng := &mockEngineV2{
		drivers: []core.DriverStatus{{Name: "plc1", Type: "modbus-tcp"}},
	}
	// Empty secret = no auth middleware
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/drivers", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with empty secret, got %d", resp.StatusCode)
	}
	var result struct {
		Drivers []core.DriverStatus `json:"drivers"`
	}
	decodeJSON(t, resp, &result)
	if len(result.Drivers) != 1 {
		t.Errorf("expected 1 driver, got %d", len(result.Drivers))
	}
}

func TestDisableRuleBadJSON(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	req, _ := http.NewRequest("PATCH", ts.URL+"/rules/disable", bytes.NewReader([]byte("bad")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}
