package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// --- ParseWithPath tests ---

func TestParseWithPathValid(t *testing.T) {
	// Create a temp config file.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.yaml")
	content := `
global:
  log-level: info
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "0.0.0.0"
        type: float32
        interval: "1s"
transports:
  - name: t1
    type: mqtt
    broker: "tcp://localhost:1883"
    topic: "test/topic"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := ParseWithPath(cfgPath)
	if err != nil {
		t.Fatalf("ParseWithPath failed: %v", err)
	}
	if len(cfg.Drivers) != 1 || cfg.Drivers[0].Name != "d1" {
		t.Errorf("unexpected config: %+v", cfg.Drivers)
	}
}

func TestParseWithPathNonExistent(t *testing.T) {
	_, err := ParseWithPath("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

// --- resolveConfigPath tests ---

func TestResolveConfigPathValid(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	Init(&mockEngineForExecutor{}, &core.Config{}, basePath)

	// A simple filename in the same directory should resolve.
	resolved, err := resolveConfigPath("new.yaml")
	if err != nil {
		t.Fatalf("resolveConfigPath failed: %v", err)
	}
	expected := filepath.Join(dir, "new.yaml")
	if resolved != expected {
		t.Errorf("expected %s, got %s", expected, resolved)
	}
}

func TestResolveConfigPathSubdirectory(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	Init(&mockEngineForExecutor{}, &core.Config{}, basePath)

	resolved, err := resolveConfigPath("subdir/new.yaml")
	if err != nil {
		t.Fatalf("resolveConfigPath failed: %v", err)
	}
	expected := filepath.Join(dir, "subdir", "new.yaml")
	if resolved != expected {
		t.Errorf("expected %s, got %s", expected, resolved)
	}
}

func TestResolveConfigPathTraversalAttack(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	Init(&mockEngineForExecutor{}, &core.Config{}, basePath)

	// Path traversal attempt should be rejected.
	_, err := resolveConfigPath("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal attempt")
	}
}

func TestResolveConfigPathAbsoluteRejected(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	Init(&mockEngineForExecutor{}, &core.Config{}, basePath)

	_, err := resolveConfigPath("/etc/passwd")
	if err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestResolveConfigPathNoBase(t *testing.T) {
	Init(&mockEngineForExecutor{}, &core.Config{}, "")

	_, err := resolveConfigPath("new.yaml")
	if err == nil {
		t.Error("expected error when no base config path is set")
	}
}

// --- Reload from file path tests ---

func TestReloadFromFilePath(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	baseContent := `
global:
  log-level: info
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "0.0.0.0"
        type: float32
        interval: "1s"
transports:
  - name: t1
    type: mqtt
    broker: "tcp://localhost:1883"
    topic: "test"
`
	if err := os.WriteFile(basePath, []byte(baseContent), 0o644); err != nil {
		t.Fatalf("failed to write base config: %v", err)
	}

	eng := &mockEngineForExecutor{}
	Init(eng, &core.Config{}, basePath)

	// Create a new config file in the same directory.
	newPath := filepath.Join(dir, "new.yaml")
	newContent := `
global:
  log-level: debug
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "0.0.0.0"
        type: float32
        interval: "1s"
transports:
  - name: t1
    type: mqtt
    broker: "tcp://localhost:1883"
    topic: "test2"
`
	if err := os.WriteFile(newPath, []byte(newContent), 0o644); err != nil {
		t.Fatalf("failed to write new config: %v", err)
	}

	// Reload from the new file (relative path).
	if err := Reload("new.yaml", ""); err != nil {
		t.Fatalf("Reload from file failed: %v", err)
	}

	// Verify config was updated.
	cfg := CurrentConfig()
	if cfg == nil || cfg.Global.LogLevel != "debug" {
		t.Errorf("expected log-level debug after reload, got: %+v", cfg)
	}
}

func TestReloadNoPathNoPayload(t *testing.T) {
	Init(&mockEngineForExecutor{}, &core.Config{}, "")

	// No path and no payload — should error.
	err := Reload("", "")
	if err == nil {
		t.Error("expected error when no path and no payload")
	}
}

func TestReloadInvalidPayload(t *testing.T) {
	Init(&mockEngineForExecutor{}, &core.Config{}, "config.yaml")

	err := Reload("", "invalid: yaml: [[[[")
	if err == nil {
		t.Error("expected error for invalid YAML payload")
	}
}

func TestReloadTraversalPath(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	Init(&mockEngineForExecutor{}, &core.Config{}, basePath)

	// Path traversal should be rejected.
	err := Reload("../../../etc/passwd", "")
	if err == nil {
		t.Error("expected error for path traversal in Reload")
	}
}

// --- Patch tests ---

func TestPatchUnknownKey(t *testing.T) {
	Init(&mockEngineForExecutor{}, &core.Config{}, "config.yaml")

	err := Patch(map[string]any{"unknown-key": "value"})
	if err == nil {
		t.Error("expected error for unknown patch key")
	}
}

func TestPatchInvalidLogLevel(t *testing.T) {
	Init(&mockEngineForExecutor{}, &core.Config{}, "config.yaml")

	err := Patch(map[string]any{"log-level": "invalid-level"})
	if err == nil {
		t.Error("expected error for invalid log level")
	}
}

func TestPatchEmptyLogLevel(t *testing.T) {
	Init(&mockEngineForExecutor{}, &core.Config{}, "config.yaml")

	// Empty log-level should be a no-op (no error).
	err := Patch(map[string]any{"log-level": ""})
	if err != nil {
		t.Errorf("expected no error for empty log-level, got: %v", err)
	}
}

func TestPatchMultipleKeys(t *testing.T) {
	Init(&mockEngineForExecutor{}, &core.Config{}, "config.yaml")

	err := Patch(map[string]any{"log-level": "debug", "other": "value"})
	if err == nil {
		t.Error("expected error for multiple unknown keys")
	}
}

// --- ApplyConfig tests ---

func TestApplyConfigNilEngine(t *testing.T) {
	// Reset to nil engine.
	Init(nil, &core.Config{}, "config.yaml")

	err := ApplyConfig(&core.Config{}, false)
	if err == nil {
		t.Error("expected error when engine is nil")
	}
}

func TestApplyConfigForceFlag(t *testing.T) {
	eng := &mockEngineForExecutor{}
	initialCfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "modbus-tcp"},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mqtt"},
		},
	}
	Init(eng, initialCfg, "config.yaml")

	// Same config with force=true should still reload drivers.
	newCfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "modbus-tcp"},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mqtt"},
		},
	}

	if err := ApplyConfig(newCfg, true); err != nil {
		t.Fatalf("ApplyConfig with force failed: %v", err)
	}

	// With force, d1 should be removed and re-added.
	foundRemoved := false
	for _, name := range eng.removedDrivers {
		if name == "d1" {
			foundRemoved = true
		}
	}
	if !foundRemoved {
		t.Error("expected d1 to be removed with force=true")
	}

	foundAdded := false
	for _, name := range eng.addedDrivers {
		if name == "d1" {
			foundAdded = true
		}
	}
	if !foundAdded {
		t.Error("expected d1 to be re-added with force=true")
	}
}

func TestApplyConfigTransportDiff(t *testing.T) {
	eng := &mockEngineForExecutor{}
	initialCfg := &core.Config{
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mqtt"},
			{Name: "t2", Type: "mqtt"},
		},
	}
	Init(eng, initialCfg, "config.yaml")

	// Remove t2, keep t1.
	newCfg := &core.Config{
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mqtt"},
		},
	}

	if err := ApplyConfig(newCfg, false); err != nil {
		t.Fatalf("ApplyConfig failed: %v", err)
	}

	// t2 should be removed (RemoveTransport called).
	// The mock doesn't track transport removals, but no error means success.
}

// --- Path A: Validate dry-run + sentinel-merge tests ---

// liveConfigForValidate is a valid config with a real api.secret that the
// sentinel-merge can backfill from when a redacted payload is validated.
func liveConfigForValidate() *core.Config {
	return &core.Config{
		Global: core.GlobalConfig{
			LogLevel: "info",
			API:      core.APIConfig{Listen: "0.0.0.0:9090", Secret: "live-secret-2026-abc"},
		},
		Drivers: []core.DriverConfig{{
			Name: "plc1", Type: "modbus-tcp",
			Settings: map[string]any{"host": "10.0.0.5", "port": 502, "password": "live-driver-pwd"},
			Tags:     []core.TagConfig{{Name: "temp", Address: "40001", Type: "float32"}},
		}},
		Transports: []core.TransportConfig{{
			Name: "wh1", Type: "http",
			Settings: map[string]any{"webhook-addr": "0.0.0.0:9091", "webhook-secret": "live-wh-secret"},
		}},
		Rules: []core.RuleConfig{{Name: "r1", Match: "ALL", Action: "forward"}},
	}
}

// TestValidate_RedactedPayloadMergesAndValidates proves the executor's dry-run
// Validate merges "***" sentinels against currentCfg BEFORE validating, so a
// config round-tripped from GET /configs/raw validates successfully (the real
// api.secret is restored, passing the min-length check that would reject "***").
func TestValidate_RedactedPayloadMergesAndValidates(t *testing.T) {
	Init(&mockEngineForExecutor{}, liveConfigForValidate(), "config.yaml")

	// A redacted payload: api.secret and driver password are "***" (what the
	// operator gets from GET /configs/raw and submits unchanged).
	redactedYAML := `
global:
  log-level: info
  api:
    listen: 0.0.0.0:9090
    secret: "***"
drivers:
  - name: plc1
    type: modbus-tcp
    settings:
      host: 10.0.0.5
      port: 502
      password: "***"
    tags:
      - name: temp
        address: "40001"
        type: float32
transports:
  - name: wh1
    type: http
    settings:
      webhook-addr: 0.0.0.0:9091
      webhook-secret: "***"
rules:
  - name: r1
    match: ALL
    action: forward
`
	if _, err := Validate(redactedYAML); err != nil {
		t.Fatalf("Validate should pass after merge restores real secrets, got: %v", err)
	}
}

// TestValidate_RotatedSecretValidates proves an operator-changed secret (not
// "***") passes through the merge untouched and validates on its own merits.
func TestValidate_RotatedSecretValidates(t *testing.T) {
	Init(&mockEngineForExecutor{}, liveConfigForValidate(), "config.yaml")

	// Operator rotates the api.secret to a new 8+ char value; leaves the
	// driver password as "***" (unchanged → backfilled).
	rotatedYAML := `
global:
  log-level: info
  api:
    listen: 0.0.0.0:9090
    secret: "new-rotated-secret-2027"
drivers:
  - name: plc1
    type: modbus-tcp
    settings:
      host: 10.0.0.5
      port: 502
      password: "***"
    tags:
      - name: temp
        address: "40001"
        type: float32
transports:
  - name: wh1
    type: http
    settings:
      webhook-addr: 0.0.0.0:9091
      webhook-secret: "***"
rules:
  - name: r1
    match: ALL
    action: forward
`
	if _, err := Validate(rotatedYAML); err != nil {
		t.Fatalf("Validate with rotated secret should pass, got: %v", err)
	}
}

// TestValidate_TooShortRotatedSecretFails proves the dry-run catches a genuine
// validation error: if the operator types a NEW secret that's too short (< 8),
// the merge leaves it untouched and validate rejects it. This confirms the
// dry-run is a faithful predictor, not a rubber-stamp.
func TestValidate_TooShortRotatedSecretFails(t *testing.T) {
	Init(&mockEngineForExecutor{}, liveConfigForValidate(), "config.yaml")

	tooShortYAML := `
global:
  log-level: info
  api:
    listen: 0.0.0.0:9090
    secret: "short"
drivers:
  - name: plc1
    type: modbus-tcp
    settings:
      host: 10.0.0.5
      port: 502
    tags:
      - name: temp
        address: "40001"
        type: float32
transports:
  - name: wh1
    type: http
    settings:
      webhook-addr: 0.0.0.0:9091
rules:
  - name: r1
    match: ALL
    action: forward
`
	_, err := Validate(tooShortYAML)
	if err == nil {
		t.Fatal("expected validation error for too-short rotated secret, got nil")
	}
}

// TestValidate_EmptyPayload proves the empty-payload guard.
func TestValidate_EmptyPayload(t *testing.T) {
	Init(&mockEngineForExecutor{}, liveConfigForValidate(), "config.yaml")
	if _, err := Validate(""); err == nil {
		t.Error("expected error for empty payload")
	}
}
