// Package config provides configuration loading, parsing, and validation for CoreC.
package config

import (
	"fmt"
	"os"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/goccy/go-yaml"
)

// globalDriverRegistry wraps core's package-level functions to satisfy
// the DriverRegistry interface, keeping validate() decoupled from
// global state (ARCH-002).
type globalDriverRegistry struct{}

func (globalDriverRegistry) RegisteredDrivers() []string { return core.RegisteredDrivers() }

// globalTransportRegistry wraps core's package-level functions to satisfy
// the TransportRegistry interface (ARCH-002).
type globalTransportRegistry struct{}

func (globalTransportRegistry) RegisteredTransports() []string { return core.RegisteredTransports() }

// Load reads, parses, and validates a YAML configuration file. Used at
// startup (cmd/corec) where there is no current config and no sentinel
// placeholders, so parse and validate can run inline.
func Load(path string) (*core.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}
	return Parse(data)
}

// LoadNoValidate reads and parses a YAML configuration file WITHOUT running
// validation. Used by the executor's Reload path, which must run the
// sentinel-merge (config.MergeSentinels) BETWEEN parse and validate: a config
// round-tripped from GET /configs/raw carries "***" for unchanged secrets, and
// validate rejects short sentinels (e.g. api.secret < 8 chars). Merging first
// restores the real secret values, then Validate runs against the faithful
// config the operator is about to apply.
func LoadNoValidate(path string) (*core.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}
	return ParseNoValidate(data)
}

// Parse parses YAML bytes into a Config, running the full validation rules.
// Used at startup and anywhere a freshly authored config (no sentinel
// placeholders) is loaded.
func Parse(data []byte) (*core.Config, error) {
	cfg, err := ParseNoValidate(data)
	if err != nil {
		return nil, err
	}
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	return cfg, nil
}

// ParseNoValidate parses YAML bytes into a Config WITHOUT running validation.
func ParseNoValidate(data []byte) (*core.Config, error) {
	expanded, err := expandEnvVars(data)
	if err != nil {
		return nil, err
	}

	var cfg core.Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if err := loadTagsFiles(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate runs the full validation rules against a parsed Config using
// the global driver and transport registries.
func Validate(cfg *core.Config) error {
	return validate(cfg, globalDriverRegistry{}, globalTransportRegistry{})
}

// ValidateWithRegistries runs validation against a Config using the
// provided driver and transport registries. Used in tests to avoid
// depending on global registration state (ARCH-002).
func ValidateWithRegistries(cfg *core.Config, drivers DriverRegistry, transports TransportRegistry) error {
	return validate(cfg, drivers, transports)
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
		// Expand ${ENV_VAR} placeholders safely in tags file.
		expanded, err := expandEnvVars(data)
		if err != nil {
			return fmt.Errorf("driver %s: failed to expand env vars in tags file %s: %w", dc.Name, dc.TagsFile, err)
		}
		var fileTags []core.TagConfig
		if err := yaml.Unmarshal(expanded, &fileTags); err != nil {
			return fmt.Errorf("driver %s: failed to parse tags file %s: %w", dc.Name, dc.TagsFile, err)
		}
		// File tags first, then inline tags appended.
		dc.Tags = append(fileTags, dc.Tags...)
	}
	return nil
}
