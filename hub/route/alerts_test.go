package route

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// alertTestServer is an authless test server with an alert-capable
// mock engine preloaded with one active alert.
func alertTestServer(t *testing.T) (*httptest.Server, *mockEngineV2) {
	t.Helper()
	now := time.Now()
	eng := &mockEngineV2{
		alerts: []core.Alert{
			{Key: "hot|plc1|temp", Rule: "hot", Driver: "plc1", Tag: "temp",
				FirstFired: now.Add(-time.Minute), LastFired: now.Add(-time.Second), Count: 5},
			{Key: "low|plc2|hum", Rule: "low", Driver: "plc2", Tag: "hum",
				FirstFired: now.Add(-2 * time.Minute), LastFired: now.Add(-2 * time.Second),
				Count: 1, Acked: true, AckedAt: now.Add(-time.Minute)},
		},
		alertHistory: []core.AlertEvent{
			{Type: core.AlertAcked, Alert: core.Alert{Key: "low|plc2|hum"}, At: now.Add(-time.Minute)},
			{Type: core.AlertFired, Alert: core.Alert{Key: "hot|plc1|temp"}, At: now.Add(-time.Minute)},
		},
		alertFireCounts: map[string]uint64{"hot": 5, "low": 1},
	}
	return newTestServer("", eng), eng
}

// TestGetAlerts verifies the GET /alerts payload shape (audit
// OBS-4/HUB-6): active alerts (acknowledged included but flagged),
// newest-first history, and the active/acked counts.
func TestGetAlerts(t *testing.T) {
	ts, _ := alertTestServer(t)
	defer ts.Close()

	resp := authedGet(t, ts, "/alerts", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Alerts      []core.Alert      `json:"alerts"`
		History     []core.AlertEvent `json:"history"`
		ActiveCount int               `json:"active_count"`
		AckedCount  int               `json:"acked_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Alerts) != 2 || body.ActiveCount != 2 {
		t.Errorf("alerts = %+v, active_count = %d, want 2/2", body.Alerts, body.ActiveCount)
	}
	if body.AckedCount != 1 {
		t.Errorf("acked_count = %d, want 1", body.AckedCount)
	}
	if len(body.History) != 2 || body.History[0].Type != core.AlertAcked {
		t.Errorf("history = %+v, want newest-first with acked first", body.History)
	}
}

// TestAlertsNotSupported verifies an engine without the AlertProvider
// role answers 501 instead of 500 or an empty 200.
func TestAlertsNotSupported(t *testing.T) {
	// mockEngineV2 implements the roles, so build a bare mock that does
	// not: embed only core.Engine (nil).
	bare := struct{ core.Engine }{}
	ts := newTestServer("", bare)
	defer ts.Close()

	resp := authedGet(t, ts, "/alerts", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("GET /alerts status = %d, want 501", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/alerts/ack", strings.NewReader(`{"key":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp3, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotImplemented {
		t.Errorf("POST /alerts/ack status = %d, want 501", resp3.StatusCode)
	}
}

// TestAckAlert verifies POST /alerts/ack: 204 on success, 404 for an
// unknown key (stale client state), 400 for a missing key, and 501
// without the role.
func TestAckAlert(t *testing.T) {
	ts, eng := alertTestServer(t)
	defer ts.Close()

	do := func(body string) int {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/alerts/ack", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := do(`{"key":"hot|plc1|temp"}`); got != http.StatusNoContent {
		t.Errorf("ack known key status = %d, want 204", got)
	}
	eng.ackErr = errors.New("no active alert with key \"missing\"") // simulates unknown key
	if got := do(`{"key":"missing"}`); got != http.StatusNotFound {
		t.Errorf("ack unknown key status = %d, want 404", got)
	}
	if got := do(`{}`); got != http.StatusBadRequest {
		t.Errorf("ack empty body status = %d, want 400", got)
	}
}

// TestAlertsStreamWebSocket verifies WS /alerts/stream forwards alert
// lifecycle events as JSON.
func TestAlertsStreamWebSocket(t *testing.T) {
	eng := &mockEngineV2{
		alertStreamCh: make(chan core.AlertEvent, 4),
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/alerts/stream"
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

	// Push a fired event through the mock's stream channel; the WS
	// handler must deliver it as JSON.
	eng.alertStreamCh <- core.AlertEvent{
		Type: core.AlertFired,
		Alert: core.Alert{
			Key: "hot|plc1|temp", Rule: "hot", Driver: "plc1", Tag: "temp",
			Value: 95, FirstFired: time.Now(), LastFired: time.Now(), Count: 1,
		},
		At: time.Now(),
	}

	var ev struct {
		Type  string     `json:"type"`
		Alert core.Alert `json:"alert"`
	}
	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readCancel()
	if err := wsjson.Read(readCtx, c, &ev); err != nil {
		t.Fatalf("wsjson.Read: %v", err)
	}
	if ev.Type != "fired" {
		t.Errorf("event type = %q, want fired", ev.Type)
	}
	if ev.Alert.Key != "hot|plc1|temp" || ev.Alert.Count != 1 {
		t.Errorf("alert = %+v, want hot|plc1|temp with count 1", ev.Alert)
	}
}

// TestPromMetricsAlerts verifies the alert metric families render from
// the AlertMetricsProvider role.
func TestPromMetricsAlerts(t *testing.T) {
	ts, _ := alertTestServer(t)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	body := string(bodyBytes)

	checks := []string{
		"corec_alerts_total{rule=\"hot\"} 5",
		"corec_alerts_total{rule=\"low\"} 1",
		"corec_alerts_active 2",
	}
	for _, s := range checks {
		if !strings.Contains(body, s) {
			t.Errorf("metrics output missing %q\nOutput:\n%s", s, body)
		}
	}
}
