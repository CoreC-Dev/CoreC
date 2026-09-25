package config

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"

	// Import all drivers and transports so the registry is populated
	// and validate() can perform fail-fast type checking (M10).
	_ "github.com/CoreC-Dev/CoreC/driver/all"
	_ "github.com/CoreC-Dev/CoreC/transport/all"
)

func TestParseValidConfig(t *testing.T) {
	yamlContent := `
global:
  log-level: debug

drivers:
  - name: modbus1
    type: modbus-tcp
    tags:
      - name: temp
        address: 40001
        type: float32

transports:
  - name: mqtt1
    type: mqtt

rules:
  - name: rule1
    match: ALL
    action: forward
    target: mqtt1
`
	cfg, err := Parse([]byte(yamlContent))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(cfg.Drivers) != 1 || cfg.Drivers[0].Name != "modbus1" {
		t.Errorf("unexpected drivers: %+v", cfg.Drivers)
	}
	if len(cfg.Transports) != 1 || cfg.Transports[0].Name != "mqtt1" {
		t.Errorf("unexpected transports: %+v", cfg.Transports)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Name != "rule1" {
		t.Errorf("unexpected rules: %+v", cfg.Rules)
	}
}

func TestValidateConfigErrors(t *testing.T) {
	validTag := core.TagConfig{Name: "t1", Address: "40001", Type: "float32"}
	tests := []struct {
		name    string
		cfg     *core.Config
		wantErr string
	}{
		{
			name:    "empty drivers and no inbound transport",
			cfg:     &core.Config{},
			wantErr: "no data source: configure at least one driver, or at least one transport as inbound consumer (mqtt data-topic / http webhook-addr) for relay mode, or enable auto-discovery with node.subscribe",
		},
		{
			name: "empty drivers with non-inbound transport",
			cfg: &core.Config{
				Transports: []core.TransportConfig{{Name: "t1", Type: "mqtt"}},
			},
			wantErr: "no data source: configure at least one driver, or at least one transport as inbound consumer (mqtt data-topic / http webhook-addr) for relay mode, or enable auto-discovery with node.subscribe",
		},
		{
			name: "empty transports",
			cfg: &core.Config{
				Drivers: []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
			},
			wantErr: "at least one transport must be configured",
		},
		{
			name: "duplicate driver",
			cfg: &core.Config{
				Drivers: []core.DriverConfig{
					{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}},
					{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}},
				},
				Transports: []core.TransportConfig{{Name: "t1", Type: "mqtt"}},
			},
			wantErr: "duplicate driver name: d1",
		},
		{
			name: "invalid rule target",
			cfg: &core.Config{
				Drivers: []core.DriverConfig{
					{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}},
				},
				Transports: []core.TransportConfig{{Name: "t1", Type: "mqtt"}},
				Rules: []core.RuleConfig{
					{Name: "r1", Match: "ALL", Action: "forward", Target: "unknown"},
				},
			},
			wantErr: "rule r1: target transport \"unknown\" not found",
		},
		{
			name: "api listen set but secret empty",
			cfg: &core.Config{
				Global: core.GlobalConfig{
					API: core.APIConfig{Listen: "0.0.0.0:9090", Secret: ""},
				},
				Drivers:    []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
				Transports: []core.TransportConfig{{Name: "t1", Type: "mqtt"}},
			},
			wantErr: "api.secret is required when api.listen is set",
		},
		{
			name: "batch-size misplaced inside settings map",
			cfg: &core.Config{
				Drivers: []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
				Transports: []core.TransportConfig{
					{Name: "t1", Type: "mqtt", Settings: map[string]any{"batch-size": 50}},
				},
			},
			wantErr: `transport t1: field "batch-size" must be a top-level transport field (sibling of ` + "`settings`" + `), not an entry inside ` + "`settings`" + `; move it out one indentation level`,
		},
		{
			name: "flush-interval misplaced inside settings map",
			cfg: &core.Config{
				Drivers: []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
				Transports: []core.TransportConfig{
					{Name: "t1", Type: "http", Settings: map[string]any{"flush-interval": "1s"}},
				},
			},
			wantErr: `transport t1: field "flush-interval" must be a top-level transport field (sibling of ` + "`settings`" + `), not an entry inside ` + "`settings`" + `; move it out one indentation level`,
		},
		{
			name: "retry-count snake_case variant misplaced inside settings map",
			cfg: &core.Config{
				Drivers: []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
				Transports: []core.TransportConfig{
					{Name: "t1", Type: "http", Settings: map[string]any{"retry_count": 3}},
				},
			},
			wantErr: `transport t1: field "retry_count" must be a top-level transport field (sibling of ` + "`settings`" + `), not an entry inside ` + "`settings`" + `; move it out one indentation level`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(tt.cfg)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("expected error %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateAPIConfig(t *testing.T) {
	validTag := core.TagConfig{Name: "t1", Address: "40001", Type: "float32"}
	base := func() *core.Config {
		return &core.Config{
			Drivers:    []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
			Transports: []core.TransportConfig{{Name: "t1", Type: "mqtt"}},
		}
	}

	t.Run("listen and secret set is valid", func(t *testing.T) {
		cfg := base()
		cfg.Global.API = core.APIConfig{Listen: "0.0.0.0:9090", Secret: "s3cret-token"}
		if err := validate(cfg); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("no api config is valid", func(t *testing.T) {
		if err := validate(base()); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("listen set without secret errors", func(t *testing.T) {
		cfg := base()
		cfg.Global.API = core.APIConfig{Listen: "0.0.0.0:9090"}
		err := validate(cfg)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if err.Error() != "api.secret is required when api.listen is set" {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestValidateRelayMode(t *testing.T) {
	t.Run("zero drivers with mqtt data-topic is valid", func(t *testing.T) {
		cfg := &core.Config{
			Transports: []core.TransportConfig{
				{Name: "relay", Type: "mqtt", Settings: map[string]any{"data-topic": "upstream/#"}},
			},
		}
		if err := validate(cfg); err != nil {
			t.Fatalf("expected no error for relay node, got %v", err)
		}
	})

	t.Run("zero drivers with http webhook-addr is valid", func(t *testing.T) {
		cfg := &core.Config{
			Transports: []core.TransportConfig{
				{Name: "relay", Type: "http", Settings: map[string]any{"webhook-addr": "0.0.0.0:9091"}},
			},
		}
		if err := validate(cfg); err != nil {
			t.Fatalf("expected no error for relay node, got %v", err)
		}
	})

	t.Run("zero drivers with auto-discovery subscribe is valid", func(t *testing.T) {
		cfg := &core.Config{
			Node: core.NodeConfig{ID: "relay-B", Role: "relay", Subscribe: []string{"edge-A"}},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{"broker": "tcp://broker:1883"}},
			},
		}
		if err := validate(cfg); err != nil {
			t.Fatalf("expected no error for auto-discovery relay node, got %v", err)
		}
	})

	t.Run("zero drivers with node.id but no subscribe is invalid", func(t *testing.T) {
		cfg := &core.Config{
			Node: core.NodeConfig{ID: "relay-B", Role: "relay"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{"broker": "tcp://broker:1883"}},
			},
		}
		err := validate(cfg)
		if err == nil {
			t.Fatal("expected error for relay with no data source and no subscribe, got nil")
		}
	})
}
