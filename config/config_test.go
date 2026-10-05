package config

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"

	// Import all drivers and transports so the registry is populated
	// and ValidateWithRegistries() can perform fail-fast type checking (M10).
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
		// Note: "no data source" and "no transport" are no longer hard
		// validation errors — an empty/API-only config is a valid startup
		// state for the dashboard-driven workflow. See TestValidateEmptyAllowed
		// and TestIdleWarnings below.
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
			err := ValidateWithRegistries(tt.cfg, globalDriverRegistry{}, globalTransportRegistry{})
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
		if err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("no api config is valid", func(t *testing.T) {
		if err := ValidateWithRegistries(base(), globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("listen set without secret errors", func(t *testing.T) {
		cfg := base()
		cfg.Global.API = core.APIConfig{Listen: "0.0.0.0:9090"}
		err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{})
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
		if err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error for relay node, got %v", err)
		}
	})

	t.Run("zero drivers with http webhook-addr is valid", func(t *testing.T) {
		cfg := &core.Config{
			Transports: []core.TransportConfig{
				{Name: "relay", Type: "http", Settings: map[string]any{"webhook-addr": "0.0.0.0:9091"}},
			},
		}
		if err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
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
		if err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error for auto-discovery relay node, got %v", err)
		}
	})

	t.Run("zero drivers with node.id but no subscribe is valid (idle mode)", func(t *testing.T) {
		// Presence of a data source is no longer a hard error (dashboard-driven
		// workflow). A node with an ID but no subscribe, no drivers, and no
		// inbound transport is a valid idle state — it broadcasts heartbeats
		// but produces no data until configured via the Dashboard.
		cfg := &core.Config{
			Node: core.NodeConfig{ID: "relay-B", Role: "relay"},
			Transports: []core.TransportConfig{
				{Name: "mqtt", Type: "mqtt", Settings: map[string]any{"broker": "tcp://broker:1883"}},
			},
		}
		if err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error for idle relay node, got %v", err)
		}
	})
}

// TestValidateEmptyAllowed asserts that empty / API-only / partial configs
// pass validation. These were hard errors before the dashboard-driven
// workflow relaxed the data-source/transport presence requirement.
func TestValidateEmptyAllowed(t *testing.T) {
	validTag := core.TagConfig{Name: "t1", Address: "40001", Type: "float32"}

	t.Run("completely empty config", func(t *testing.T) {
		if err := ValidateWithRegistries(&core.Config{}, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error for empty config, got %v", err)
		}
	})

	t.Run("API-only config (no drivers, no transports)", func(t *testing.T) {
		cfg := &core.Config{
			Global: core.GlobalConfig{
				API: core.APIConfig{Listen: "0.0.0.0:9090", Secret: "secret123"},
			},
		}
		if err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error for API-only config, got %v", err)
		}
	})

	t.Run("driver but no transport", func(t *testing.T) {
		cfg := &core.Config{
			Drivers: []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
		}
		if err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error for driver-only config, got %v", err)
		}
	})

	t.Run("non-inbound transport but no driver", func(t *testing.T) {
		cfg := &core.Config{
			Transports: []core.TransportConfig{{Name: "t1", Type: "mqtt"}},
		}
		if err := ValidateWithRegistries(cfg, globalDriverRegistry{}, globalTransportRegistry{}); err != nil {
			t.Fatalf("expected no error for transport-only config, got %v", err)
		}
	})
}

// TestIdleWarnings asserts the non-blocking idle-mode warnings.
func TestIdleWarnings(t *testing.T) {
	validTag := core.TagConfig{Name: "t1", Address: "40001", Type: "float32"}
	apiCfg := core.GlobalConfig{API: core.APIConfig{Listen: ":9090", Secret: "secret123"}}

	t.Run("empty config warns about api.listen, data source and transport", func(t *testing.T) {
		w := IdleWarnings(&core.Config{})
		if len(w) != 3 {
			t.Fatalf("expected 3 warnings for empty config, got %d: %v", len(w), w)
		}
	})

	t.Run("API-only config warns about data source and transport", func(t *testing.T) {
		cfg := &core.Config{Global: apiCfg}
		if len(IdleWarnings(cfg)) != 2 {
			t.Fatalf("expected 2 warnings for API-only config, got %v", IdleWarnings(cfg))
		}
	})

	t.Run("driver only warns about api.listen and no transport", func(t *testing.T) {
		cfg := &core.Config{
			Drivers: []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
		}
		w := IdleWarnings(cfg)
		if len(w) != 2 {
			t.Fatalf("expected 2 warnings (api.listen + no transport), got %d: %v", len(w), w)
		}
	})

	t.Run("inbound transport only warns about api.listen", func(t *testing.T) {
		cfg := &core.Config{
			Transports: []core.TransportConfig{
				{Name: "t1", Type: "mqtt", Settings: map[string]any{"data-topic": "up/#"}},
			},
		}
		w := IdleWarnings(cfg)
		if len(w) != 1 {
			t.Fatalf("expected 1 warning (api.listen), got %v", w)
		}
	})

	t.Run("driver plus transport warns about api.listen", func(t *testing.T) {
		cfg := &core.Config{
			Drivers:    []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
			Transports: []core.TransportConfig{{Name: "t1", Type: "mqtt"}},
		}
		w := IdleWarnings(cfg)
		if len(w) != 1 {
			t.Fatalf("expected 1 warning (api.listen), got %v", w)
		}
	})

	t.Run("full config with api.listen — no warnings", func(t *testing.T) {
		cfg := &core.Config{
			Global:     apiCfg,
			Drivers:    []core.DriverConfig{{Name: "d1", Type: "modbus-tcp", Tags: []core.TagConfig{validTag}}},
			Transports: []core.TransportConfig{{Name: "t1", Type: "mqtt"}},
		}
		if len(IdleWarnings(cfg)) != 0 {
			t.Fatalf("expected 0 warnings for full config, got %v", IdleWarnings(cfg))
		}
	})

	t.Run("nil config — no panic", func(t *testing.T) {
		if w := IdleWarnings(nil); w != nil {
			t.Fatalf("expected nil for nil config, got %v", w)
		}
	})
}
