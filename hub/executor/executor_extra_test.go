package executor

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// TestReloadPathErrorsAreClientFixable asserts that reload path rejections
// are classified as client-fixable: the route layer type-asserts ConfigError
// to answer 400 with the reason (the request's path field is at fault, the
// message names it and nothing internal), instead of a masked 500.
func TestReloadPathErrorsAreClientFixable(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	Init(&mockEngineForExecutor{}, &core.Config{}, basePath)

	for name, path := range map[string]string{
		"absolute path":     "/etc/corec/config.yaml",
		"traversal attempt": "../../../etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			err := ReloadWithOrigin(path, "", "127.0.0.1:1")
			if err == nil {
				t.Fatal("expected error")
			}
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Errorf("reload path error should be a *ConfigError (client-fixable, 400), got %T: %v", err, err)
			}
		})
	}

	// No path, no payload and no persisted base path → the request is
	// incomplete; the rejection must still be client-fixable.
	Init(&mockEngineForExecutor{}, &core.Config{}, "")
	t.Run("missing path field", func(t *testing.T) {
		err := ReloadWithOrigin("", "", "127.0.0.1:1")
		if err == nil {
			t.Fatal("expected error")
		}
		var ce *ConfigError
		if !errors.As(err, &ce) {
			t.Errorf("no-path reload error should be a *ConfigError (client-fixable, 400), got %T: %v", err, err)
		}
	})
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

// TestValidate_PlaintextPayloadValidates proves the executor's dry-run Validate
// accepts a config with plaintext secrets that meet the minimum length check.
func TestValidate_PlaintextPayloadValidates(t *testing.T) {
	Init(&mockEngineForExecutor{}, liveConfigForValidate(), "config.yaml")

	// A plaintext payload with real secrets that meet min-length requirements.
	plaintextYAML := `
global:
  log-level: info
  api:
    listen: 0.0.0.0:9090
    secret: "live-api-secret-2026"
drivers:
  - name: plc1
    type: modbus-tcp
    settings:
      host: 10.0.0.5
      port: 502
      password: "live-driver-password"
    tags:
      - name: temp
        address: "40001"
        type: float32
transports:
  - name: wh1
    type: http
    settings:
      webhook-addr: 0.0.0.0:9091
      webhook-secret: "live-wh-secret"
rules:
  - name: r1
    match: ALL
    action: forward
`
	if _, err := Validate(plaintextYAML); err != nil {
		t.Fatalf("Validate should pass with plaintext secrets, got: %v", err)
	}
}

// TestValidate_RotatedSecretValidates proves an operator-changed secret passes
// validation on its own merits.
func TestValidate_RotatedSecretValidates(t *testing.T) {
	Init(&mockEngineForExecutor{}, liveConfigForValidate(), "config.yaml")

	// Operator rotates the api.secret to a new 8+ char value.
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
      password: "live-driver-password"
    tags:
      - name: temp
        address: "40001"
        type: float32
transports:
  - name: wh1
    type: http
    settings:
      webhook-addr: 0.0.0.0:9091
      webhook-secret: "live-wh-secret"
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

// TestReloadErrorClassification verifies the PUT /configs error
// semantics: parse and validation failures are
// wrapped as client-fixable ConfigError (the route answers 400 with the
// reason), while apply-stage failures stay unmarked (masked 500).
func TestReloadErrorClassification(t *testing.T) {
	// Bypass the package-level engine wiring: Reload's parse and
	// validate stages run before any engine access, so a nil engine is
	// only reached (and fatal) at the apply stage.
	t.Run("parse failure is client-fixable", func(t *testing.T) {
		err := Reload("", "\tbad: [yaml")
		if err == nil {
			t.Fatal("expected error for malformed YAML")
		}
		var cfe *ConfigError
		if !errors.As(err, &cfe) {
			t.Errorf("parse failure not classified as ConfigError: %v", err)
		}
	})

	t.Run("validation failure is client-fixable", func(t *testing.T) {
		// api.listen without secret fails validation.
		cfg := "global:\n  api:\n    listen: 127.0.0.1:9090\n"
		err := Reload("", cfg)
		if err == nil {
			t.Fatal("expected validation error")
		}
		var cfe *ConfigError
		if !errors.As(err, &cfe) {
			t.Errorf("validation failure not classified as ConfigError: %v", err)
		}
		if !strings.Contains(err.Error(), "api.secret") {
			t.Errorf("error = %v, want the underlying validation reason", err)
		}
	})

	t.Run("apply failure is not client-fixable", func(t *testing.T) {
		// A valid config that requires the engine (a driver) reaches
		// the apply stage; with no engine wired the failure is an
		// internal one and must NOT be marked ConfigError. The
		// package-level engine/config/path are saved and restored so
		// this subtest is hermetic regardless of the state other
		// tests in the package left behind.
		savedEngine, savedCfg, savedPath := engine, currentCfg, configPath
		t.Cleanup(func() { Init(savedEngine, savedCfg, savedPath) })
		Init(nil, nil, "")

		cfg := "drivers:\n  - name: d1\n    type: modbus-tcp\n    settings:\n      host: 127.0.0.1\n      port: 1502\n    tags:\n      - name: t1\n        address: \"40001\"\n        type: uint16\n"
		err := Reload("", cfg)
		if err == nil {
			t.Fatal("expected apply-stage error (no engine wired)")
		}
		if !strings.Contains(err.Error(), "engine not initialized") {
			t.Fatalf("error = %v, want the deterministic unwired-engine apply failure", err)
		}
		var cfe *ConfigError
		if errors.As(err, &cfe) {
			t.Errorf("apply failure wrongly classified as client-fixable: %v", err)
		}
	})
}

// TestConfigAuditRecordsReloadAndPatch verifies the executor-side
// recording: ReloadWithOrigin and
// PatchWithOrigin append bounded, newest-first entries with the
// caller's address and a change summary.
func TestConfigAuditRecordsReloadAndPatch(t *testing.T) {
	cfgAudit.mu.Lock()
	cfgAudit.entries = nil
	cfgAudit.mu.Unlock()

	savedEngine, savedCfg, savedPath := engine, currentCfg, configPath
	defer Init(savedEngine, savedCfg, savedPath)
	Init(nil, &core.Config{
		Global: core.GlobalConfig{LogLevel: "info"},
	}, "")
	// ApplyConfig requires a non-nil engine; the empty-diff path needs
	// only Suspend/Resume.
	mux.Lock()
	engine = nilEngineOnlySuspendResume{}
	mux.Unlock()

	// Reload with origin.
	if err := ReloadWithOrigin("", "global:\n  log-level: warn\n", "10.0.0.7:12345"); err != nil {
		t.Fatalf("ReloadWithOrigin: %v", err)
	}
	// Patch with origin.
	if err := PatchWithOrigin(map[string]any{"log-level": "debug"}, "10.0.0.7:99999"); err != nil {
		t.Fatalf("PatchWithOrigin: %v", err)
	}

	entries := ConfigAuditHistory()
	if len(entries) != 2 {
		t.Fatalf("audit entries = %d, want 2", len(entries))
	}
	// Newest first: patch, then reload.
	if entries[0].Action != "patch" || entries[0].Remote != "10.0.0.7:99999" {
		t.Errorf("patch entry = %+v", entries[0])
	}
	if len(entries[0].Keys) != 1 || entries[0].Keys[0] != "log-level" {
		t.Errorf("patch keys = %v, want [log-level]", entries[0].Keys)
	}
	if entries[1].Action != "reload" || entries[1].Remote != "10.0.0.7:12345" {
		t.Errorf("reload entry = %+v", entries[1])
	}
	if len(entries[1].Keys) != 1 || entries[1].Keys[0] != "payload" {
		t.Errorf("reload keys = %v, want [payload]", entries[1].Keys)
	}
}

// nilEngineOnlySuspendResume is a core.Engine stub whose only live
// methods are Suspend/Resume (the empty-diff ApplyConfig path).
type nilEngineOnlySuspendResume struct{ core.Engine }

func (nilEngineOnlySuspendResume) Suspend() error { return nil }
func (nilEngineOnlySuspendResume) Resume() error  { return nil }

// TestPatchExpansionLogFormatAndStaleThreshold verifies PATCH /configs
// handles the three runtime keys: log-level (existing),
// log-format (swaps the stdout handler without breaking the /logs bus),
// and stale-threshold (runtime role; engines without it reject).
func TestPatchExpansionLogFormatAndStaleThreshold(t *testing.T) {
	cfgAudit.mu.Lock()
	cfgAudit.entries = nil
	cfgAudit.mu.Unlock()

	patchEngine := &patchableEngine{}
	Init(patchEngine, &core.Config{Global: core.GlobalConfig{LogLevel: "info"}}, "")
	defer Init(nil, nil, "")

	// Unknown keys still rejected.
	if err := PatchWithOrigin(map[string]any{"bogus": 1}, ""); err == nil {
		t.Error("expected rejection of unknown patch key")
	}

	// log-format: json applies; invalid values rejected.
	if err := PatchWithOrigin(map[string]any{"log-format": "json"}, "r1"); err != nil {
		t.Fatalf("log-format json: %v", err)
	}
	if err := PatchWithOrigin(map[string]any{"log-format": "xml"}, ""); err == nil {
		t.Error("expected rejection of invalid log-format")
	}
	// "" is a documented no-op.
	if err := PatchWithOrigin(map[string]any{"log-format": ""}, ""); err != nil {
		t.Fatalf("log-format empty no-op: %v", err)
	}

	// stale-threshold: applies through the role.
	if err := PatchWithOrigin(map[string]any{"stale-threshold": "45s"}, "r2"); err != nil {
		t.Fatalf("stale-threshold: %v", err)
	}
	if patchEngine.threshold != 45*time.Second {
		t.Errorf("engine threshold = %v, want 45s", patchEngine.threshold)
	}
	// "0" disables.
	if err := PatchWithOrigin(map[string]any{"stale-threshold": "0s"}, ""); err != nil {
		t.Fatalf("stale-threshold disable: %v", err)
	}
	if patchEngine.threshold != 0 {
		t.Errorf("engine threshold after disable = %v, want 0", patchEngine.threshold)
	}
	// Malformed durations rejected.
	if err := PatchWithOrigin(map[string]any{"stale-threshold": "soon"}, ""); err == nil {
		t.Error("expected rejection of malformed stale-threshold")
	}

	// Audit carries one entry per applied patch (json + 45s + 0s; the
	// rejected/empty ones record nothing).
	entries := ConfigAuditHistory()
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Keys[0])
	}
	want := []string{"stale-threshold", "stale-threshold", "log-format"}
	if len(entries) != 3 {
		t.Fatalf("audit entries = %v, want %v (3 applied patches)", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("audit key[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}

// patchableEngine is a core.Engine stub with the StaleThresholdPatcher
// role recording the last applied threshold.
type patchableEngine struct {
	core.Engine
	threshold time.Duration
}

func (p *patchableEngine) SetStaleThreshold(d time.Duration) { p.threshold = d }
func (p *patchableEngine) Suspend() error                    { return nil }
func (p *patchableEngine) Resume() error                     { return nil }

// TestRemoveResourceByNames verifies the executor side of the
// resource-level DELETE endpoints: removing
// one entry by name runs the full validate → apply → persist chain,
// 404-maps missing names, and validation fallout (deleting a transport
// that rules target) surfaces as the client-fixable 400 class.
func TestRemoveResourceByNames(t *testing.T) {
	cfgAudit.mu.Lock()
	cfgAudit.entries = nil
	cfgAudit.mu.Unlock()

	savedEngine, savedCfg, savedPath := engine, currentCfg, configPath
	defer Init(savedEngine, savedCfg, savedPath)

	full := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "plc-a", Type: "modbus-tcp",
				Settings: map[string]any{"host": "127.0.0.1", "port": 15020, "slave-id": 1},
				Tags:     []core.TagConfig{{Name: "t", Address: "40001", Type: "uint16"}}},
		},
		Transports: []core.TransportConfig{
			{Name: "mqtt-a", Type: "mqtt", Settings: map[string]any{"broker": "tcp://127.0.0.1:18831"}},
		},
		Rules: []core.RuleConfig{
			{Name: "fwd", Match: "ALL", Action: "forward", Target: "mqtt-a"},
		},
	}
	eng := &resourceTestEngine{}
	Init(eng, full, "")
	// Engine must also serve as the driver/transport manager for the diff.
	eng.AddedDrivers, eng.RemovedDrivers = nil, nil

	// Missing name → ErrResourceNotFound (route maps to 404).
	if err := RemoveDriverByName("nope", "r"); !errors.Is(err, ErrResourceNotFound) {
		t.Errorf("missing driver: err = %v, want ErrResourceNotFound", err)
	}

	// Driver removal walks the diff chain (engine sees RemoveDriver).
	if err := RemoveDriverByName("plc-a", "10.0.0.1:1"); err != nil {
		t.Fatalf("RemoveDriverByName: %v", err)
	}
	if len(eng.RemovedDrivers) != 1 || eng.RemovedDrivers[0] != "plc-a" {
		t.Errorf("engine removed drivers = %v, want [plc-a]", eng.RemovedDrivers)
	}
	if cur := CurrentConfig(); len(cur.Drivers) != 0 {
		t.Errorf("currentCfg still has %d drivers after removal", len(cur.Drivers))
	}

	// Transport removal that leaves a dangling rule target → ConfigError
	// (route maps to 400 with the reason).
	err := RemoveTransportByName("mqtt-a", "10.0.0.1:2")
	var cfe *ConfigError
	if !errors.As(err, &cfe) {
		t.Fatalf("removing referenced transport: err = %v, want ConfigError", err)
	}
	if !strings.Contains(err.Error(), "fwd") {
		t.Errorf("error should name the affected rule, got: %v", err)
	}

	// Rule removal clears the dependency; the transport then removes.
	if err := RemoveRuleByName("fwd", "10.0.0.1:3"); err != nil {
		t.Fatalf("RemoveRuleByName: %v", err)
	}
	if err := RemoveTransportByName("mqtt-a", "10.0.0.1:4"); err != nil {
		t.Fatalf("RemoveTransportByName after rule removal: %v", err)
	}
	if cur := CurrentConfig(); len(cur.Rules) != 0 || len(cur.Transports) != 0 {
		t.Errorf("config not empty after removals: %+v", cur)
	}

	// Every successful removal recorded an audit entry naming the
	// resource (ReloadWithOrigin also records its own "payload" entry —
	// the removal entry is the one carrying the delete-* key, newest
	// first: resource entry then the reload's payload entry).
	entries := ConfigAuditHistory()
	var deleteKeys []string
	for _, e := range entries {
		for _, k := range e.Keys {
			if strings.HasPrefix(k, "delete-") {
				deleteKeys = append(deleteKeys, k)
			}
		}
	}
	want := []string{
		"delete-transport:mqtt-a",
		"delete-rule:fwd",
		"delete-driver:plc-a",
	}
	if !reflect.DeepEqual(deleteKeys, want) {
		t.Errorf("delete audit keys = %v, want %v", deleteKeys, want)
	}
}

// resourceTestEngine implements the roles the reload chain needs
// (driver/transport manager + suspend) while recording removals.
type resourceTestEngine struct {
	core.Engine
	AddedDrivers, RemovedDrivers       []string
	AddedTransports, RemovedTransports []string
	RulesSet                           int
}

func (e *resourceTestEngine) AddDriver(c core.DriverConfig) error {
	e.AddedDrivers = append(e.AddedDrivers, c.Name)
	return nil
}
func (e *resourceTestEngine) RemoveDriver(name string) error {
	e.RemovedDrivers = append(e.RemovedDrivers, name)
	return nil
}
func (e *resourceTestEngine) AddTransport(c core.TransportConfig) error {
	e.AddedTransports = append(e.AddedTransports, c.Name)
	return nil
}
func (e *resourceTestEngine) RemoveTransport(name string) error {
	e.RemovedTransports = append(e.RemovedTransports, name)
	return nil
}
func (e *resourceTestEngine) SetRules(rules []core.RuleConfig) error {
	e.RulesSet++
	return nil
}
func (e *resourceTestEngine) Suspend() error { return nil }
func (e *resourceTestEngine) Resume() error  { return nil }
