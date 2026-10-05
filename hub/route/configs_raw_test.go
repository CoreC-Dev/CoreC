package route

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/config"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/goccy/go-yaml"
)

// TestGetConfigsRaw verifies GET /configs/raw returns the full config as YAML
// with secrets redacted to "***" and never leaks the real secret values.
func TestGetConfigsRaw(t *testing.T) {
	const realSecret = "do-not-leak-this-token-2026"
	const realPwd = "super-private-mqtt-password"

	SetEngine(&mockEngine{})
	defer func() {
		GetRawConfigFunc = nil
		GetConfigFunc = nil
		SetEngine(nil)
	}()

	// Return a config carrying real secrets; the handler must redact them.
	GetRawConfigFunc = func() (string, error) {
		cfg := &core.Config{
			Global: core.GlobalConfig{
				LogLevel: "info",
				API:      core.APIConfig{Listen: "0.0.0.0:9090", Secret: realSecret},
			},
			Transports: []core.TransportConfig{{
				Name: "mqtt1", Type: "mqtt",
				Settings: map[string]any{
					"broker":   "ssl://broker:8883",
					"password": realPwd,
				},
			}},
			Rules: []core.RuleConfig{{Name: "r1", Match: "ALL", Action: "forward"}},
		}
		// Use the real Redact path to prove the wired func produces redacted YAML.
		redacted, err := config.Redact(cfg)
		if err != nil {
			return "", err
		}
		data, err := yaml.Marshal(redacted)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	ts := httptest.NewServer(router(context.Background(), "secret-123", nil, 0, true))
	defer ts.Close()

	// 1. Without auth → 401 (the raw config is in the authenticated group).
	resp, err := http.Get(ts.URL + "/configs/raw")
	if err != nil {
		t.Fatalf("GET /configs/raw: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth GET /configs/raw: got %d, want 401", resp.StatusCode)
	}

	// 2. With auth → 200 + application/yaml + redacted.
	req, _ := http.NewRequest("GET", ts.URL+"/configs/raw", http.NoBody)
	req.Header.Set("Authorization", "Bearer secret-123")
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatalf("authed GET /configs/raw: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /configs/raw: got %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("Content-Type: got %q, want application/yaml*", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	yamlText := string(body)

	// Red-team: real secrets must NOT appear in the response body.
	if strings.Contains(yamlText, realSecret) {
		t.Errorf("GET /configs/raw leaked api.secret %q", realSecret)
	}
	if strings.Contains(yamlText, realPwd) {
		t.Errorf("GET /configs/raw leaked transport password %q", realPwd)
	}
	// Sentinel must appear so the operator sees the secret is configured.
	if !strings.Contains(yamlText, "***") {
		t.Errorf("GET /configs/raw missing sentinel *** for redacted secrets")
	}
	// Non-secret config must survive so the editor shows real config.
	if !strings.Contains(yamlText, "ssl://broker:8883") {
		t.Errorf("GET /configs/raw dropped non-secret broker field")
	}
}

// TestValidateConfigs verifies POST /configs/validate dry-runs a config payload
// without applying it: valid → 200 {valid:true}; invalid → 400 {valid:false}.
func TestValidateConfigs(t *testing.T) {
	SetEngine(&mockEngine{})
	defer func() {
		ValidateFunc = nil
		SetEngine(nil)
	}()

	// ValidateFunc mirrors the executor: parse+validate only, no apply.
	ValidateFunc = func(payload string) (error, []string) {
		_, err := config.Parse([]byte(payload))
		return err, nil
	}

	ts := httptest.NewServer(router(context.Background(), "secret-123", nil, 0, true))
	defer ts.Close()

	// 1. Valid config → 200 {valid: true}. Includes a driver (data source) and
	// an HTTP webhook transport (satisfies "at least one transport" + inbound).
	validYAML := "node:\n  id: edge\n  role: collector\nglobal:\n  log-level: info\n  api:\n    listen: 0.0.0.0:9090\n    secret: valid-secret-123\ndrivers:\n  - name: plc1\n    type: modbus-tcp\n    settings:\n      host: 192.168.1.5\n      port: 502\n    tags:\n      - name: temp\n        address: \"40001\"\n        type: float32\ntransports:\n  - name: wh1\n    type: http\n    settings:\n      webhook-addr: 0.0.0.0:9091\n      webhook-path: /data\nrules:\n  - name: r1\n    match: ALL\n    action: forward\n"
	body, _ := json.Marshal(map[string]string{"payload": validYAML})
	req, _ := http.NewRequest("POST", ts.URL+"/configs/validate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-123")
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /configs/validate (valid): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid payload: got %d, want 200", resp.StatusCode)
	}
	var ok map[string]any
	json.NewDecoder(resp.Body).Decode(&ok)
	if ok["valid"] != true {
		t.Errorf("valid payload: response valid=%v, want true", ok["valid"])
	}

	// 2. Invalid config (api.secret too short) → 400 {valid: false, error: ...}.
	// Note: "no data source / no transport" is no longer invalid — an empty
	// config is a valid idle state. Use a hard, registry-independent failure
	// (secret length) so the outcome does not depend on driver registration.
	invalidYAML := "global:\n  api:\n    listen: 0.0.0.0:9090\n    secret: short\n"
	body, _ = json.Marshal(map[string]string{"payload": invalidYAML})
	req, _ = http.NewRequest("POST", ts.URL+"/configs/validate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-123")
	req.Header.Set("Content-Type", "application/json")
	resp2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /configs/validate (invalid): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid payload: got %d, want 400", resp2.StatusCode)
	}
	var bad map[string]any
	json.NewDecoder(resp2.Body).Decode(&bad)
	if bad["valid"] != false {
		t.Errorf("invalid payload: response valid=%v, want false", bad["valid"])
	}
	if _, hasErr := bad["error"]; !hasErr {
		t.Errorf("invalid payload: response missing error message")
	}
}

// TestValidateConfigs_NoAuth verifies the validate endpoint requires auth.
func TestValidateConfigs_NoAuth(t *testing.T) {
	ValidateFunc = func(string) (error, []string) { return nil, nil }
	defer func() { ValidateFunc = nil }()
	ts := httptest.NewServer(router(context.Background(), "secret-123", nil, 0, true))
	defer ts.Close()
	body, _ := json.Marshal(map[string]string{"payload": "x"})
	resp, err := http.Post(ts.URL+"/configs/validate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth validate: got %d, want 401", resp.StatusCode)
	}
}
