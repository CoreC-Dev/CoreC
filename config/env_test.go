// Package config — environment variable substitution tests.
package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// Import all drivers and transports so the registry is populated
	// and validate() can perform fail-fast type checking.
	_ "github.com/CoreC-Dev/CoreC/driver/all"
	_ "github.com/CoreC-Dev/CoreC/transport/all"
)

// asInt extracts an int from a YAML-parsed numeric value. The goccy/go-yaml
// library may return int, int64, uint64, or float64 depending on the value
// magnitude and sign, so we handle all cases.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func TestExpandEnvVars_BasicSubstitution(t *testing.T) {
	t.Setenv("COREC_TEST_SECRET", "my-secret-value")

	data := []byte(`
global:
  log-level: info
  api:
    listen: "0.0.0.0:9090"
    secret: ${COREC_TEST_SECRET}
drivers:
  - name: plc
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502, slave-id: 1 }
    tags:
      - { name: temp, address: "40001", type: float32 }
transports:
  - name: mqtt1
    type: mqtt
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	expanded := expandEnvVars(data)
	cfg, err := Parse(expanded)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.Global.API.Secret != "my-secret-value" {
		t.Errorf("expected secret 'my-secret-value', got %q", cfg.Global.API.Secret)
	}
}

func TestExpandEnvVars_MultipleVars(t *testing.T) {
	t.Setenv("COREC_TEST_HOST", "192.168.1.100")
	t.Setenv("COREC_TEST_BROKER", "tcp://broker.example.com:1883")

	data := []byte(`
global:
  log-level: info
drivers:
  - name: plc
    type: modbus-tcp
    settings:
      host: ${COREC_TEST_HOST}
      port: 502
      slave-id: 1
    tags:
      - { name: temp, address: "40001", type: float32 }
transports:
  - name: mqtt1
    type: mqtt
    settings:
      broker: ${COREC_TEST_BROKER}
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	host := cfg.Drivers[0].Settings["host"]
	if host != "192.168.1.100" {
		t.Errorf("expected host '192.168.1.100', got %v", host)
	}
	broker := cfg.Transports[0].Settings["broker"]
	if broker != "tcp://broker.example.com:1883" {
		t.Errorf("expected broker 'tcp://broker.example.com:1883', got %v", broker)
	}
}

func TestExpandEnvVars_UnsetLeftAsIs(t *testing.T) {
	// Ensure the variable is not set.
	os.Unsetenv("COREC_DEFINITELY_UNSET_VAR")

	data := []byte(`
global:
  log-level: info
  api:
    listen: "0.0.0.0:9090"
    secret: ${COREC_DEFINITELY_UNSET_VAR}
drivers:
  - name: plc
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags:
      - { name: temp, address: "40001", type: float32 }
transports:
  - name: mqtt1
    type: mqtt
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	expanded := expandEnvVars(data)

	// The placeholder should remain as-is in the raw data.
	if !strings.Contains(string(expanded), "${COREC_DEFINITELY_UNSET_VAR}") {
		t.Errorf("expected placeholder to remain in data, got: %s", expanded)
	}

	// Parse should succeed; the secret will be the literal placeholder
	// string (which is >8 chars, passing the length check).
	cfg, err := Parse(expanded)
	if err != nil {
		t.Fatalf("Parse failed unexpectedly: %v", err)
	}
	if cfg.Global.API.Secret != "${COREC_DEFINITELY_UNSET_VAR}" {
		t.Errorf("expected literal placeholder, got %q", cfg.Global.API.Secret)
	}
}

func TestExpandEnvVars_TypeInferenceInt(t *testing.T) {
	t.Setenv("COREC_TEST_PORT", "502")

	data := []byte(`
global:
  log-level: info
drivers:
  - name: plc
    type: modbus-tcp
    settings:
      host: 192.168.1.100
      port: ${COREC_TEST_PORT}
      slave-id: 1
    tags:
      - { name: temp, address: "40001", type: float32 }
transports:
  - name: mqtt1
    type: mqtt
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	// The port should be parsed as a numeric type, not a string, because
	// byte-level substitution produces "port: 502" which YAML infers as int.
	port := cfg.Drivers[0].Settings["port"]
	n, ok := asInt(port)
	if !ok {
		t.Errorf("expected port to be numeric, got %T (%v)", port, port)
	} else if n != 502 {
		t.Errorf("expected port 502, got %d", n)
	}
}

func TestExpandEnvVars_InsideQuotedString(t *testing.T) {
	t.Setenv("COREC_TEST_NODE", "edge-A")

	data := []byte(`
global:
  log-level: info
drivers:
  - name: plc
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags:
      - { name: temp, address: "40001", type: float32 }
transports:
  - name: mqtt1
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: "topo/${COREC_TEST_NODE}/data/{{.Tag}}"
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	topic := cfg.Transports[0].Settings["topic-template"]
	if topic != "topo/edge-A/data/{{.Tag}}" {
		t.Errorf("expected 'topo/edge-A/data/{{.Tag}}', got %v", topic)
	}
}

func TestExpandEnvVars_NoPlaceholders(t *testing.T) {
	data := []byte(`
global:
  log-level: info
transports:
  - name: mqtt1
    type: mqtt
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	expanded := expandEnvVars(data)
	// Should return the original data unchanged (fast path).
	if !bytes.Equal(expanded, data) {
		t.Errorf("expected data unchanged when no placeholders present")
	}
}

func TestExpandEnvVars_VarNameValidation(t *testing.T) {
	t.Setenv("COREC_TEST_VAR_123", "value123")
	t.Setenv("_UNDERSCORE_START", "underscore_val")

	data := []byte("v1: ${COREC_TEST_VAR_123}\nv2: ${_UNDERSCORE_START}")
	expanded := expandEnvVars(data)

	if !strings.Contains(string(expanded), "value123") {
		t.Errorf("expected 'value123' in expanded data, got: %s", expanded)
	}
	if !strings.Contains(string(expanded), "underscore_val") {
		t.Errorf("expected 'underscore_val' in expanded data, got: %s", expanded)
	}
}

func TestExpandEnvVars_TagsFile(t *testing.T) {
	t.Setenv("COREC_TEST_ADDR", "40099")

	// Create a temporary tags file with an env var placeholder.
	dir := t.TempDir()
	tagsPath := filepath.Join(dir, "tags.yaml")
	tagsContent := []byte(`
- name: env_tag
  address: ${COREC_TEST_ADDR}
  type: float32
`)
	if err := os.WriteFile(tagsPath, tagsContent, 0o644); err != nil {
		t.Fatal(err)
	}

	data := []byte(`
global:
  log-level: info
drivers:
  - name: plc
    type: modbus-tcp
    tags-file: ` + tagsPath + `
    settings: { host: 192.168.1.100, port: 502, slave-id: 1 }
transports:
  - name: mqtt1
    type: mqtt
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(cfg.Drivers[0].Tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(cfg.Drivers[0].Tags))
	}
	if cfg.Drivers[0].Tags[0].Address != "40099" {
		t.Errorf("expected address '40099', got %q", cfg.Drivers[0].Tags[0].Address)
	}
}

func TestExpandEnvVars_EmptyValue(t *testing.T) {
	// An explicitly-set empty env var should be replaced with empty string.
	t.Setenv("COREC_TEST_EMPTY", "")

	data := []byte("v: ${COREC_TEST_EMPTY}")
	expanded := expandEnvVars(data)

	// The result should be "v: " (empty value after colon-space).
	if string(expanded) != "v: " {
		t.Errorf("expected 'v: ', got %q", string(expanded))
	}
}

func TestExpandEnvVars_ReuseAcrossFields(t *testing.T) {
	t.Setenv("COREC_TEST_BROKER", "tcp://broker:1883")

	data := []byte(`
global:
  log-level: info
drivers:
  - name: plc
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags:
      - { name: temp, address: "40001", type: float32 }
transports:
  - name: mqtt1
    type: mqtt
    settings:
      broker: ${COREC_TEST_BROKER}
  - name: mqtt2
    type: mqtt
    settings:
      broker: ${COREC_TEST_BROKER}
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	for i, tr := range cfg.Transports {
		broker := tr.Settings["broker"]
		if broker != "tcp://broker:1883" {
			t.Errorf("transport %d: expected broker 'tcp://broker:1883', got %v", i, broker)
		}
	}
}

// Verify that config validation still works correctly after env var
// expansion — an unset variable leaves the placeholder, which passes
// validation (it's a long enough string) but is visibly wrong.
func TestExpandEnvVars_ValidationStillWorks(t *testing.T) {
	// Don't set the env var, so the placeholder remains.
	os.Unsetenv("COREC_TEST_MISSING_SECRET")

	data := []byte(`
global:
  api:
    listen: "0.0.0.0:9090"
    secret: ${COREC_TEST_MISSING_SECRET}
drivers:
  - name: plc
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags:
      - { name: temp, address: "40001", type: float32 }
transports:
  - name: mqtt1
    type: mqtt
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	// The placeholder ${COREC_TEST_MISSING_SECRET} is 31 chars (>8),
	// so validation passes the length check. The literal placeholder
	// remains, making the misconfiguration visible to operators.
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.Global.API.Secret != "${COREC_TEST_MISSING_SECRET}" {
		t.Errorf("expected literal placeholder, got %q", cfg.Global.API.Secret)
	}
}

// Ensure env var expansion works with a full valid config including
// intervals, durations, and nested settings.
func TestExpandEnvVars_FullConfigIntegration(t *testing.T) {
	t.Setenv("COREC_TEST_HOST", "10.0.0.50")
	t.Setenv("COREC_TEST_SLAVE", "7")
	t.Setenv("COREC_TEST_TOPIC", "factory/sensor/temp")

	data := []byte(`
global:
  log-level: info
  api:
    listen: "0.0.0.0:9090"
    secret: "test-secret-123"
drivers:
  - name: plc
    type: modbus-tcp
    settings:
      host: ${COREC_TEST_HOST}
      port: 502
      slave-id: ${COREC_TEST_SLAVE}
    tags:
      - name: temperature
        address: "40001"
        type: float32
        interval: 1s
transports:
  - name: mqtt1
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: ${COREC_TEST_TOPIC}
rules:
  - { name: r1, match: ALL, action: forward, target: mqtt1 }
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if cfg.Drivers[0].Settings["host"] != "10.0.0.50" {
		t.Errorf("expected host '10.0.0.50', got %v", cfg.Drivers[0].Settings["host"])
	}

	// slave-id should be numeric, not a string, because byte-level
	// substitution produces "slave-id: 7" which YAML infers as a number.
	slaveID := cfg.Drivers[0].Settings["slave-id"]
	n, ok := asInt(slaveID)
	if !ok {
		t.Errorf("expected slave-id to be numeric, got %T (%v)", slaveID, slaveID)
	} else if n != 7 {
		t.Errorf("expected slave-id 7, got %d", n)
	}

	if cfg.Transports[0].Settings["topic-template"] != "factory/sensor/temp" {
		t.Errorf("expected topic 'factory/sensor/temp', got %v", cfg.Transports[0].Settings["topic-template"])
	}

	// Verify the tag interval still parses correctly.
	if cfg.Drivers[0].Tags[0].Interval != "1s" {
		t.Errorf("expected interval '1s', got %q", cfg.Drivers[0].Tags[0].Interval)
	}
	if _, err := time.ParseDuration(cfg.Drivers[0].Tags[0].Interval); err != nil {
		t.Errorf("interval parse failed: %v", err)
	}

	// Smoke-test the core types are correct.
	if cfg.Drivers[0].Tags[0].Type != "float32" {
		t.Errorf("expected float32, got %v", cfg.Drivers[0].Tags[0].Type)
	}
}
