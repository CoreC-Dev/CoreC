// Package config provides configuration loading, parsing, and validation for CoreC.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/goccy/go-yaml"
)

// Load reads and parses a YAML configuration file.
func Load(path string) (*core.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}
	return Parse(data)
}

// Parse parses YAML bytes into a Config.
func Parse(data []byte) (*core.Config, error) {
	cfg := &core.Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	// Load tags from external files for drivers that use tags-file.
	if err := loadTagsFiles(cfg); err != nil {
		return nil, fmt.Errorf("failed to load tags files: %w", err)
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	return cfg, nil
}

// loadTagsFiles reads external tag files referenced by drivers via the
// tags-file field and merges the loaded tags into each driver's Tags slice.
// Inline tags (from the Tags field) are appended after file-loaded tags.
// Tag name uniqueness across both sources is enforced by validate().
func loadTagsFiles(cfg *core.Config) error {
	for i := range cfg.Drivers {
		dc := &cfg.Drivers[i]
		if dc.TagsFile == "" {
			continue
		}
		data, err := os.ReadFile(dc.TagsFile)
		if err != nil {
			return fmt.Errorf("driver %s: failed to read tags file %s: %w", dc.Name, dc.TagsFile, err)
		}
		var fileTags []core.TagConfig
		if err := yaml.Unmarshal(data, &fileTags); err != nil {
			return fmt.Errorf("driver %s: failed to parse tags file %s: %w", dc.Name, dc.TagsFile, err)
		}
		// File tags first, then inline tags appended.
		dc.Tags = append(fileTags, dc.Tags...)
	}
	return nil
}

// validate performs config validation, failing fast on invalid values
// that would otherwise only surface at runtime.
func validate(cfg *core.Config) error {
	// Validate offline buffer configuration if enabled.
	if cfg.Global.Buffer.Enabled {
		if cfg.Global.Buffer.Path == "" {
			return fmt.Errorf("buffer.enabled is true but buffer.path is not set")
		}
		if cfg.Global.Buffer.MaxSize > 0 && cfg.Global.Buffer.MaxSize < 10 {
			return fmt.Errorf("buffer.max-size must be at least 10, got %d", cfg.Global.Buffer.MaxSize)
		}
	}

	// A node needs at least one data source. That is normally a driver,
	// but a pure relay (chained-core) node has no drivers and instead
	// receives data via an inbound transport (MQTT data-topic or HTTP
	// webhook-addr). Allow zero drivers when:
	//   - an explicit inbound transport exists, OR
	//   - auto-discovery is enabled (node.id set) with a non-empty subscribe list
	//     (the discovery module will auto-create inbound transports at runtime).
	if len(cfg.Drivers) == 0 && !hasInboundTransport(cfg.Transports) && !hasAutoDiscoveryInbound(cfg) {
		return fmt.Errorf("no data source: configure at least one driver, or at least one transport as inbound consumer (mqtt data-topic / http webhook-addr) for relay mode, or enable auto-discovery with node.subscribe")
	}
	if len(cfg.Transports) == 0 {
		return fmt.Errorf("at least one transport must be configured")
	}

	// Collect registered driver/transport types for fail-fast validation (M10).
	registeredDrivers := make(map[string]bool)
	for _, t := range core.RegisteredDrivers() {
		registeredDrivers[t] = true
	}
	registeredTransports := make(map[string]bool)
	for _, t := range core.RegisteredTransports() {
		registeredTransports[t] = true
	}

	// Check driver names are unique and validate fields
	driverNames := make(map[string]bool)
	for _, d := range cfg.Drivers {
		if d.Name == "" {
			return fmt.Errorf("driver name cannot be empty")
		}
		if driverNames[d.Name] {
			return fmt.Errorf("duplicate driver name: %s", d.Name)
		}
		driverNames[d.Name] = true
		if d.Type == "" {
			return fmt.Errorf("driver %s: type cannot be empty", d.Name)
		}
		// M10: validate driver type against registry
		if len(registeredDrivers) > 0 && !registeredDrivers[d.Type] {
			return fmt.Errorf("driver %s: unknown type %q (registered: %s)", d.Name, d.Type, strings.Join(core.RegisteredDrivers(), ", "))
		}
		if len(d.Tags) == 0 {
			return fmt.Errorf("driver %s: at least one tag must be configured", d.Name)
		}

		// M7: validate tag fields
		tagNames := make(map[string]bool)
		for _, tag := range d.Tags {
			if tag.Name == "" {
				return fmt.Errorf("driver %s: tag name cannot be empty", d.Name)
			}
			if tagNames[tag.Name] {
				return fmt.Errorf("driver %s: duplicate tag name %q", d.Name, tag.Name)
			}
			tagNames[tag.Name] = true
			if tag.Address == "" {
				return fmt.Errorf("driver %s: tag %q: address cannot be empty", d.Name, tag.Name)
			}
			if tag.Type == "" {
				return fmt.Errorf("driver %s: tag %q: type cannot be empty", d.Name, tag.Name)
			}
			if _, ok := core.ParseDataType(tag.Type); !ok {
				return fmt.Errorf("driver %s: tag %q: invalid type %q (valid: bool, int8, int16, int32, int64, uint8, uint16, uint32, uint64, float32, float64, string, bytes)", d.Name, tag.Name, tag.Type)
			}
			if tag.Interval != "" {
				if dur, err := time.ParseDuration(tag.Interval); err != nil {
					return fmt.Errorf("driver %s: tag %q: invalid interval %q: %w", d.Name, tag.Name, tag.Interval, err)
				} else if dur <= 0 {
					return fmt.Errorf("driver %s: tag %q: interval must be positive, got %v", d.Name, tag.Name, dur)
				}
			}
			if tag.ReadTimeout != "" {
				if dur, err := time.ParseDuration(tag.ReadTimeout); err != nil {
					return fmt.Errorf("driver %s: tag %q: invalid read-timeout %q: %w", d.Name, tag.Name, tag.ReadTimeout, err)
				} else if dur <= 0 {
					return fmt.Errorf("driver %s: tag %q: read-timeout must be positive, got %v", d.Name, tag.Name, dur)
				}
			}
		}
	}

	// API: when the API server is enabled (listen address set), a shared
	// secret is mandatory to authenticate management requests. This avoids
	// accidentally exposing an unauthenticated control plane.
	if cfg.Global.API.Listen != "" {
		if cfg.Global.API.Secret == "" {
			return fmt.Errorf("api.secret is required when api.listen is set")
		}
		// Enforce a minimum secret length to resist brute-force attacks,
		// especially before rate limiting was added (and as defense-in-depth).
		if len(cfg.Global.API.Secret) < 8 {
			return fmt.Errorf("api.secret must be at least 8 characters, got %d", len(cfg.Global.API.Secret))
		}
	}

	// Check transport names are unique
	transportNames := make(map[string]bool)
	for _, t := range cfg.Transports {
		if t.Name == "" {
			return fmt.Errorf("transport name cannot be empty")
		}
		if transportNames[t.Name] {
			return fmt.Errorf("duplicate transport name: %s", t.Name)
		}
		transportNames[t.Name] = true
		if t.Type == "" {
			return fmt.Errorf("transport %s: type cannot be empty", t.Name)
		}
		// M10: validate transport type against registry
		if len(registeredTransports) > 0 && !registeredTransports[t.Type] {
			return fmt.Errorf("transport %s: unknown type %q (registered: %s)", t.Name, t.Type, strings.Join(core.RegisteredTransports(), ", "))
		}
	}

	// Valid rule actions for fail-fast validation (M8)
	validActions := map[string]bool{
		"forward": true, "drop": true, "alert": true,
		"transform": true, "mirror": true,
	}

	// Validate rules reference existing transports and have valid actions
	for _, r := range cfg.Rules {
		if r.Name == "" {
			return fmt.Errorf("rule name cannot be empty")
		}
		// M8: validate action at config time
		if r.Action == "" {
			return fmt.Errorf("rule %s: action cannot be empty", r.Name)
		}
		if !validActions[strings.ToLower(r.Action)] {
			return fmt.Errorf("rule %s: unknown action %q (valid: forward, drop, alert, transform, mirror)", r.Name, r.Action)
		}
		// M8: validate match is not empty
		if r.Match == "" {
			return fmt.Errorf("rule %s: match cannot be empty", r.Name)
		}
		if r.Target != "" && !transportNames[r.Target] {
			return fmt.Errorf("rule %s: target transport %q not found", r.Name, r.Target)
		}
		for _, t := range r.Targets {
			if !transportNames[t] {
				return fmt.Errorf("rule %s: target transport %q not found", r.Name, t)
			}
		}
	}

	return nil
}

// hasInboundTransport reports whether any transport is configured as a
// chained-core inbound data consumer — an MQTT transport with a data-topic
// or an HTTP transport with a webhook-addr. A pure relay node has no drivers
// and relies on such an inbound transport as its data source.
func hasInboundTransport(transports []core.TransportConfig) bool {
	for _, t := range transports {
		if s, ok := t.Settings["data-topic"].(string); ok && s != "" {
			return true
		}
		if s, ok := t.Settings["webhook-addr"].(string); ok && s != "" {
			return true
		}
	}
	return false
}

// hasAutoDiscoveryInbound returns true when auto-discovery is enabled
// (node.id is set) and the node declares upstream subscriptions — the
// discovery module will auto-create inbound transports at runtime.
func hasAutoDiscoveryInbound(cfg *core.Config) bool {
	return cfg.Node.ID != "" && len(cfg.Node.Subscribe) > 0
}
