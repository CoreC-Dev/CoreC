package config

import (
	"os"
	"path/filepath"
	"testing"

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
		{
			name: "no transports",
			yaml: `
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "40001"
        type: float32
`,
			wantErr: "at least one transport must be configured",
		},
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
// loads and validates successfully. The example ships with one active driver
// (plc-modbus), two active transports (cloud-mqtt, mes-http-push) and two
// active rules (high-temp-alert, default-catch-all) whose targets resolve to
// those transports, so it must be a fully valid, runnable configuration.
func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load("../config.example.yaml")
	if err != nil {
		t.Fatalf("config.example.yaml must load successfully, got: %v", err)
	}
	if len(cfg.Drivers) == 0 {
		t.Fatal("expected at least one driver in example config")
	}
	if len(cfg.Transports) == 0 {
		t.Fatal("expected at least one transport in example config")
	}
	if len(cfg.Rules) == 0 {
		t.Fatal("expected at least one rule in example config")
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
