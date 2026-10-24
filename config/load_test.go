package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	_ "github.com/CoreC-Dev/CoreC/driver/all"
	_ "github.com/CoreC-Dev/CoreC/transport/all"
)

// TestLoadValidFile verifies that Load reads a real YAML file from disk
// and returns a fully parsed and validated Config.
func TestLoadValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
global:
  log-level: debug
  api:
    listen: "0.0.0.0:9090"
    secret: "my-secret-123"

drivers:
  - name: plc1
    type: modbus-tcp
    settings:
      host: 127.0.0.1
      port: 502
    tags:
      - name: temperature
        address: "40001"
        type: float32
        interval: 1s
        group: sensors
      - name: pressure
        address: "40002"
        type: int16
        interval: 500ms

transports:
  - name: cloud-mqtt
    type: mqtt
    settings:
      broker: tcp://localhost:1883

rules:
  - name: high-temp
    match: "tag == 'temperature' && value > 90"
    action: alert
    target: cloud-mqtt
    priority: 1
  - name: forward-all
    match: ALL
    action: forward
    target: cloud-mqtt
    priority: 999
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Global.LogLevel != "debug" {
		t.Errorf("expected log-level 'debug', got %q", cfg.Global.LogLevel)
	}
	if len(cfg.Drivers) != 1 {
		t.Fatalf("expected 1 driver, got %d", len(cfg.Drivers))
	}
	if cfg.Drivers[0].Name != "plc1" {
		t.Errorf("expected driver 'plc1', got %q", cfg.Drivers[0].Name)
	}
	if len(cfg.Drivers[0].Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(cfg.Drivers[0].Tags))
	}
	if cfg.Drivers[0].Tags[0].Group != "sensors" {
		t.Errorf("expected group 'sensors', got %q", cfg.Drivers[0].Tags[0].Group)
	}
	if len(cfg.Transports) != 1 {
		t.Fatalf("expected 1 transport, got %d", len(cfg.Transports))
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cfg.Rules))
	}
	if cfg.Global.API.Listen != "0.0.0.0:9090" {
		t.Errorf("expected API listen, got %q", cfg.Global.API.Listen)
	}
}

// TestLoadNonexistentFile verifies that Load returns a clear error for
// a file that doesn't exist.
func TestLoadNonexistentFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
	// The error should mention the path.
	if !contains(err.Error(), "failed to read config file") {
		t.Errorf("expected error about reading file, got: %v", err)
	}
}

// TestLoadInvalidYAML verifies that Load returns a parse error for
// malformed YAML.
func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	content := `
drivers:
  - name: plc1
    type: modbus-tcp
    tags:
      - name: temp
        address: "40001"
        type: float32
  this is not valid yaml: [unclosed
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// TestLoadValidationErrors verifies that Load runs validation and returns
// errors for semantically invalid configs.
func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "empty driver name",
			yaml: `
drivers:
  - name: ""
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: "driver name cannot be empty",
		},
		{
			name: "empty driver type",
			yaml: `
drivers:
  - name: d1
    type: ""
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: "driver d1: type cannot be empty",
		},
		{
			name: "duplicate driver name",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
  - name: d1
    type: modbus-tcp
    tags:
      - name: t2
        address: "40002"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: "duplicate driver name: d1",
		},
		{
			name: "empty tag name",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: ""
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: "driver d1: tag name cannot be empty",
		},
		{
			name: "duplicate tag name",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
      - name: t1
        address: "40002"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: `driver d1: duplicate tag name "t1"`,
		},
		{
			name: "empty tag address",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: ""
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: `driver d1: tag "t1": address cannot be empty`,
		},
		{
			name: "invalid tag type",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: "invalid-type"
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: `driver d1: tag "t1": invalid type "invalid-type"`,
		},
		{
			name: "invalid interval",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
        interval: "not-a-duration"
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: `driver d1: tag "t1": invalid interval "not-a-duration"`,
		},
		{
			name: "negative interval",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
        interval: "-5s"
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: `driver d1: tag "t1": interval must be positive`,
		},
		// Note: "no transports" is no longer a hard error — a driver-only
		// config is a valid idle state in the dashboard-driven workflow.
		{
			name: "unknown driver type",
			yaml: `
drivers:
  - name: d1
    type: nonexistent-driver
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: `driver d1: unknown type "nonexistent-driver"`,
		},
		{
			name: "unknown transport type",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: t1
    type: nonexistent-transport
`,
			wantErr: `transport t1: unknown type "nonexistent-transport"`,
		},
		{
			name: "rule with invalid action",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
rules:
  - name: r1
    match: ALL
    action: "invalid-action"
    target: mqtt1
`,
			wantErr: `rule r1: unknown action "invalid-action"`,
		},
		{
			name: "rule with empty match",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
rules:
  - name: r1
    match: ""
    action: forward
    target: mqtt1
`,
			wantErr: "rule r1: match cannot be empty",
		},
		{
			name: "api secret too short",
			yaml: `
global:
  api:
    listen: "0.0.0.0:9090"
    secret: "short"
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`,
			wantErr: "api.secret must be at least 8 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatalf("failed to write temp config: %v", err)
			}

			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestLoadRelayMode verifies that a relay node (no drivers, inbound transport)
// loads successfully.
func TestLoadRelayMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.yaml")
	content := `
transports:
  - name: relay
    type: mqtt
    settings:
      broker: tcp://localhost:1883
      data-topic: "upstream/#"
rules:
  - name: forward-all
    match: ALL
    action: forward
    target: relay
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed for relay mode: %v", err)
	}
	if len(cfg.Drivers) != 0 {
		t.Errorf("expected 0 drivers in relay mode, got %d", len(cfg.Drivers))
	}
	if len(cfg.Transports) != 1 {
		t.Errorf("expected 1 transport, got %d", len(cfg.Transports))
	}
}

// TestLoadExampleConfig verifies that the project's own config.example.yaml
// loads and validates successfully. The file's active content is the minimal
// bootstrap config (global.api only — no drivers, transports, or rules); all
// full-example blocks are commented out and can be uncommented by the operator.
// An empty/API-only config is a valid, runnable startup state (idle mode).
func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load("../config.example.yaml")
	if err != nil {
		t.Fatalf("config.example.yaml must load successfully, got: %v", err)
	}
	// Minimal bootstrap: no business entities — the engine starts in idle mode.
	if len(cfg.Drivers) != 0 {
		t.Fatalf("expected no drivers in minimal example config, got %d", len(cfg.Drivers))
	}
	if len(cfg.Transports) != 0 {
		t.Fatalf("expected no transports in minimal example config, got %d", len(cfg.Transports))
	}
	if len(cfg.Rules) != 0 {
		t.Fatalf("expected no rules in minimal example config, got %d", len(cfg.Rules))
	}
	// Must expose the Dashboard API.
	if cfg.Global.API.Listen == "" {
		t.Fatal("expected global.api.listen to be set in minimal example config")
	}
}

// TestLoadDeprecatedBuffer verifies that the deprecated buffer section
// produces a warning but doesn't fail.
func TestLoadDeprecatedBuffer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buffer.yaml")
	content := `
global:
  buffer:
    enabled: true
    max-size: 1000
    path: /tmp/buffer
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := Load(path)
	if err != nil {
		t.Fatalf("expected warning but no error for deprecated buffer, got: %v", err)
	}
}

// TestLoadMultipleTargets verifies that rules with multiple targets (Targets slice)
// are validated correctly.
func TestLoadMultipleTargets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.yaml")
	content := `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
  - name: http1
    type: http
rules:
  - name: multi-target
    match: ALL
    action: forward
    targets:
      - mqtt1
      - http1
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(cfg.Rules))
	}
	if len(cfg.Rules[0].Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(cfg.Rules[0].Targets))
	}
}

// TestLoadMultipleTargetsInvalid verifies that an invalid target in the
// Targets slice is rejected.
func TestLoadMultipleTargetsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-targets.yaml")
	content := `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
rules:
  - name: bad-target
    match: ALL
    action: forward
    targets:
      - mqtt1
      - nonexistent
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid target, got nil")
	}
	if !contains(err.Error(), `target transport "nonexistent" not found`) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestLoadDropWithContinueRejected verifies that a drop rule with
// continue: true is rejected by validation.
func TestLoadDropWithContinueRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-continue.yaml")
	content := `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
rules:
  - name: drop-and-continue
    match: ALL
    action: drop
    continue: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for drop + continue: true, got nil")
	}
	if !contains(err.Error(), "drop action cannot set continue: true") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestLoadContinueFlag verifies that the continue flag is parsed correctly.
func TestLoadContinueFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "continue.yaml")
	content := `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
transports:
  - name: mqtt1
    type: mqtt
rules:
  - name: alert-then-continue
    match: "value > 95"
    action: alert
    target: mqtt1
    priority: 1
    continue: true
  - name: default
    match: ALL
    action: forward
    target: mqtt1
    priority: 999
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cfg.Rules))
	}
	if !cfg.Rules[0].Continue {
		t.Error("expected rule 0 to have Continue=true")
	}
	if cfg.Rules[1].Continue {
		t.Error("expected rule 1 to have Continue=false")
	}
}

// contains is a helper to check substring containment.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestValidateRejectsUnknownDriverSettings pins the reject-unknown-keys
// contract: a typo'd settings key (slve-id instead of slave-id) must
// fail validation rather than silently run the driver against
// defaults — the wrong device. The factory-constructed component's
// SettingsValidator rejects it at config-validate time with the
// valid keys listed.
func TestValidateRejectsUnknownDriverSettings(t *testing.T) {
	cfg := &core.Config{
		Drivers: []core.DriverConfig{{
			Name: "plc1", Type: "modbus-tcp",
			Settings: map[string]any{
				"host":    "127.0.0.1",
				"port":    502,
				"slve-id": 7, // typo: slave-id
			},
			Tags: []core.TagConfig{{Name: "t", Address: "40001", Type: "uint16"}},
		}},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected unknown-settings error for the typo'd key")
	}
	if !strings.Contains(err.Error(), "slve-id") || !strings.Contains(err.Error(), "slave-id") {
		t.Errorf("error should name the typo and list the valid keys, got: %v", err)
	}

	// The correctly spelled key passes.
	cfg.Drivers[0].Settings = map[string]any{"host": "127.0.0.1", "port": 502, "slave-id": 7}
	if err := Validate(cfg); err != nil {
		t.Errorf("correct settings rejected: %v", err)
	}
}

// TestValidateRejectsUnknownTransportSettings is the transport twin:
// brokr instead of broker fails validation with the valid keys listed.
func TestValidateRejectsUnknownTransportSettings(t *testing.T) {
	cfg := &core.Config{
		Transports: []core.TransportConfig{{
			Name: "cloud", Type: "mqtt",
			Settings: map[string]any{"brokr": "tcp://127.0.0.1:1883"},
		}},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected unknown-settings error for the typo'd broker key")
	}
	if !strings.Contains(err.Error(), "brokr") || !strings.Contains(err.Error(), "broker") {
		t.Errorf("error should name the typo and list the valid keys, got: %v", err)
	}
}

// TestValidateMisplacedFieldHintWinsOverAllowlist pins the ordering:
// batch-size inside settings must keep the actionable "move it out one
// indentation level" hint, not the generic unknown-settings error.
func TestValidateMisplacedFieldHintWinsOverAllowlist(t *testing.T) {
	cfg := &core.Config{
		Transports: []core.TransportConfig{{
			Name: "t1", Type: "mqtt",
			Settings: map[string]any{"broker": "tcp://127.0.0.1:1883", "batch-size": 100},
		}},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected misplaced-field error")
	}
	if !strings.Contains(err.Error(), "one indentation level") {
		t.Errorf("expected the actionable misplaced-field hint, got: %v", err)
	}
}
