// Package route implements the HTTP API for the CoreC industrial gateway.
// This file holds tests for the audit logging added to the config-, rule-,
// and tag-writing handlers. The tests verify that the slog audit records
// are emitted on both success and failure paths while preserving each
// handler's existing HTTP behaviour (status code, side effects, response
// body) — i.e. audit logging does not break existing functionality.
package route

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// captureSlog temporarily replaces the default slog logger with a text
// handler writing to an in-memory buffer, returning the buffer and a
// restore function. It lets tests assert on audit log output without
// depending on the global log bus or stdout. The previous default logger
// is restored when restore is called so other tests are unaffected.
//
// Tests in this package run sequentially (none use t.Parallel), so
// swapping the process-wide default logger is safe for the call's scope.
func captureSlog(t *testing.T) (out *bytes.Buffer, restore func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	return &buf, func() { slog.SetDefault(prev) }
}

// containsLog reports whether the captured log output contains substr.
func containsLog(buf *bytes.Buffer, substr string) bool {
	return bytes.Contains(buf.Bytes(), []byte(substr))
}

// TestUpdateConfigsAuditLog verifies that a successful PUT /configs emits
// an audit log and that the handler still returns 204 and invokes the
// reload function — i.e. audit logging did not break existing behaviour.
func TestUpdateConfigsAuditLog(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	called := false
	ReloadFunc = func(path, payload string) error {
		called = true
		return nil
	}
	defer func() { ReloadFunc = nil }()

	buf, restore := captureSlog(t)
	defer restore()

	body, _ := json.Marshal(map[string]string{"path": "newconfig.yaml"})
	req, _ := http.NewRequest("PUT", ts.URL+"/configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT /configs failed: %v", err)
	}
	defer resp.Body.Close()

	// Existing behaviour preserved.
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if !called {
		t.Error("expected ReloadFunc to be called")
	}

	// Audit record emitted with the expected fields.
	if !containsLog(buf, "config updated") {
		t.Errorf("expected audit log 'config updated', got:\n%s", buf.String())
	}
	if !containsLog(buf, "method=PUT") {
		t.Errorf("expected audit log method=PUT, got:\n%s", buf.String())
	}
	if !containsLog(buf, "config_path=newconfig.yaml") {
		t.Errorf("expected audit log config_path=newconfig.yaml, got:\n%s", buf.String())
	}
}

// TestUpdateConfigsAuditLogFailure verifies a failed reload emits an
// error-level audit log while preserving the 500 response.
func TestUpdateConfigsAuditLogFailure(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	ReloadFunc = func(path, payload string) error {
		return errors.New("config parse error")
	}
	defer func() { ReloadFunc = nil }()

	buf, restore := captureSlog(t)
	defer restore()

	body, _ := json.Marshal(map[string]string{"path": "bad.yaml"})
	req, _ := http.NewRequest("PUT", ts.URL+"/configs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT /configs failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	if !containsLog(buf, "config update failed") {
		t.Errorf("expected audit log 'config update failed', got:\n%s", buf.String())
	}
	if !containsLog(buf, "level=ERROR") {
		t.Errorf("expected ERROR-level audit log, got:\n%s", buf.String())
	}
}

// TestPatchConfigsAuditLog verifies a successful PATCH /configs emits an
// audit log and still returns 204 and forwards the patch to PatchFunc.
func TestPatchConfigsAuditLog(t *testing.T) {
	eng := &mockEngineV2{}
	ts := newTestServer("", eng)
	defer ts.Close()

	var received map[string]any
	PatchFunc = func(patch map[string]any) error {
		received = patch
		return nil
	}
	defer func() { PatchFunc = nil }()

	buf, restore := captureSlog(t)
	defer restore()

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
		t.Errorf("expected log-level=debug forwarded, got %v", received["log-level"])
	}
	if !containsLog(buf, "config patched") {
		t.Errorf("expected audit log 'config patched', got:\n%s", buf.String())
	}
	if !containsLog(buf, "method=PATCH") {
		t.Errorf("expected audit log method=PATCH, got:\n%s", buf.String())
	}
}

// TestDisableRuleAuditLog verifies the rule-disable handler emits an audit
// log naming the affected rule and still returns 204 and applies the
// disable to the rule manager.
func TestDisableRuleAuditLog(t *testing.T) {
	eng := &mockEngineV2{
		ruleStats: []core.RuleStat{{Index: 0, Name: "alert-high-temp"}},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	buf, restore := captureSlog(t)
	defer restore()

	body, _ := json.Marshal(map[string]any{"index": 0, "disabled": true})
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
	if eng.disabledIdx != 0 || !eng.disabledVal {
		t.Errorf("expected disable(0, true), got idx=%d val=%v", eng.disabledIdx, eng.disabledVal)
	}
	if !containsLog(buf, "rule disabled") {
		t.Errorf("expected audit log 'rule disabled', got:\n%s", buf.String())
	}
	if !containsLog(buf, "rule=alert-high-temp") {
		t.Errorf("expected audit log rule=alert-high-temp, got:\n%s", buf.String())
	}
}

// TestDisableRuleAuditLogEnable verifies that re-enabling a rule emits a
// "rule enabled" audit message rather than "rule disabled", so the audit
// trail reflects the actual state transition.
func TestDisableRuleAuditLogEnable(t *testing.T) {
	eng := &mockEngineV2{
		ruleStats: []core.RuleStat{{Index: 0, Name: "alert-high-temp"}},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	buf, restore := captureSlog(t)
	defer restore()

	body, _ := json.Marshal(map[string]any{"index": 0, "disabled": false})
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
	if !containsLog(buf, "rule enabled") {
		t.Errorf("expected audit log 'rule enabled', got:\n%s", buf.String())
	}
	if containsLog(buf, "rule disabled") {
		t.Errorf("did not expect 'rule disabled' for an enable, got:\n%s", buf.String())
	}
}

// TestWriteTagAuditLog verifies the write handler emits an audit log on a
// successful write and still returns the write result with 200.
func TestWriteTagAuditLog(t *testing.T) {
	eng := &mockEngineV2{
		writeResult: core.WriteResult{Success: true},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	buf, restore := captureSlog(t)
	defer restore()

	body, _ := json.Marshal(core.WriteCommand{Driver: "plc1", Tag: "temp", Value: 50})
	req, _ := http.NewRequest("POST", ts.URL+"/write", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /write failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !containsLog(buf, "tag written") {
		t.Errorf("expected audit log 'tag written', got:\n%s", buf.String())
	}
	if !containsLog(buf, "driver=plc1") {
		t.Errorf("expected audit log driver=plc1, got:\n%s", buf.String())
	}
	if !containsLog(buf, "tag=temp") {
		t.Errorf("expected audit log tag=temp, got:\n%s", buf.String())
	}
}

// TestWriteTagAuditLogFailure verifies a failed write emits an
// error-level audit log while preserving the 500 response.
func TestWriteTagAuditLogFailure(t *testing.T) {
	eng := &mockEngineV2{
		writeErr: errors.New("driver offline"),
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	buf, restore := captureSlog(t)
	defer restore()

	body, _ := json.Marshal(core.WriteCommand{Driver: "plc1", Tag: "temp", Value: 50})
	req, _ := http.NewRequest("POST", ts.URL+"/write", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /write failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	if !containsLog(buf, "tag write failed") {
		t.Errorf("expected audit log 'tag write failed', got:\n%s", buf.String())
	}
	if !containsLog(buf, "level=ERROR") {
		t.Errorf("expected ERROR-level audit log, got:\n%s", buf.String())
	}
}

// TestWriteTagAuditLogDoesNotLeakValue verifies that the audit log for a
// write records the driver and tag but NOT the written value, since the
// value may carry process-sensitive data.
func TestWriteTagAuditLogDoesNotLeakValue(t *testing.T) {
	eng := &mockEngineV2{
		writeResult: core.WriteResult{Success: true},
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	buf, restore := captureSlog(t)
	defer restore()

	body, _ := json.Marshal(core.WriteCommand{Driver: "plc1", Tag: "secret_tag", Value: "s3cr3t-p4ssw0rd"})
	req, _ := http.NewRequest("POST", ts.URL+"/write", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /write failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if containsLog(buf, "s3cr3t-p4ssw0rd") {
		t.Errorf("audit log must not contain the written value, got:\n%s", buf.String())
	}
	if !containsLog(buf, "tag=secret_tag") {
		t.Errorf("expected audit log tag=secret_tag, got:\n%s", buf.String())
	}
}
