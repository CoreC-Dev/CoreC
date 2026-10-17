package route

import (
	"net/http"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// mockEngineNoWriteAuditor is an engine that deliberately does NOT
// implement core.WriteAuditor, to exercise the 501 path of
// GET /write/history (the handler type-asserts the optional role and
// must never touch the engine otherwise).
type mockEngineNoWriteAuditor struct{ core.Engine }

// GET /write/history returns the engine's write audit history when the
// engine implements core.WriteAuditor, and 501 when it does not.

func TestGetWriteHistory(t *testing.T) {
	now := time.Now()
	eng := &mockEngineV2{
		writeHistory: []core.WriteAuditEntry{
			{
				Timestamp: now,
				Driver:    "plc1",
				Tag:       "setpoint",
				Value:     50.0,
				Type:      core.TypeFloat64,
				Success:   true,
				Source:    "api",
				Remote:    "127.0.0.1:5555",
			},
			{
				Timestamp: now.Add(-time.Second),
				Driver:    "plc1",
				Tag:       "motor_enable",
				Value:     true,
				Type:      core.TypeBool,
				Success:   false,
				Error:     "device refused write",
				Source:    "command",
			},
		},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/write/history", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result struct {
		Writes []core.WriteAuditEntry `json:"writes"`
		Count  int                    `json:"count"`
	}
	decodeJSON(t, resp, &result)
	if result.Count != 2 || len(result.Writes) != 2 {
		t.Fatalf("expected 2 writes, got count=%d len=%d", result.Count, len(result.Writes))
	}
	first := result.Writes[0]
	if first.Tag != "setpoint" || !first.Success || first.Source != "api" {
		t.Errorf("unexpected newest entry: %+v", first)
	}
	if first.Remote != "127.0.0.1:5555" {
		t.Errorf("remote: got %q, want 127.0.0.1:5555", first.Remote)
	}
	second := result.Writes[1]
	if second.Success || second.Error != "device refused write" {
		t.Errorf("unexpected failed entry: %+v (want success=false with the error message)", second)
	}
	if second.Remote != "" {
		t.Errorf("remote on command-source entry: got %q, want empty (omitempty)", second.Remote)
	}
}

func TestGetWriteHistoryEmpty(t *testing.T) {
	// The engine always returns a non-nil slice; the API must serialize
	// it as an empty JSON array, not null.
	eng := &mockEngineV2{writeHistory: []core.WriteAuditEntry{}}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/write/history", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result struct {
		Writes []core.WriteAuditEntry `json:"writes"`
		Count  int                    `json:"count"`
	}
	decodeJSON(t, resp, &result)
	if result.Count != 0 || result.Writes == nil {
		t.Errorf("expected an empty non-null writes array, got count=%d writes=%v", result.Count, result.Writes)
	}
}

func TestGetWriteHistoryNotImplemented(t *testing.T) {
	// mockEngineNoWriteAuditor lacks WriteHistory; the handler must
	// report 501 (unsupported), not 500 (broken) and not 200.
	ts := newTestServer("", &mockEngineNoWriteAuditor{})
	defer ts.Close()

	resp := authedGet(t, ts, "/write/history", "")
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}
	var errResp map[string]string
	decodeJSON(t, resp, &errResp)
	if errResp["error"] == "" {
		t.Error("expected a non-empty error message in the 501 body")
	}
}
