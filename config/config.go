// Package config provides configuration loading, parsing, and validation for CoreC.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/goccy/go-yaml"
)

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

// envVarRe matches ${ENV_VAR} placeholders in YAML data. Variable names
// must start with a letter or underscore and contain only alphanumeric
// characters and underscores, matching the common environment variable
// naming convention.
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvVars replaces ${ENV_VAR} placeholders in YAML data with
// environment variable values safely. Unlike naive byte-level replacement,
// which is vulnerable to YAML injection when env var values contain
// YAML-special characters (newlines, colons, braces), this function parses
// the YAML into a generic tree first, expands placeholders in string leaf
// values, and re-serializes — ensuring env var values are properly typed
// and escaped by the YAML encoder.
//
// Type inference is preserved: a value like ${PORT} with PORT=502 is
// converted to the integer 502 (not the string "502") so that YAML fields
// expecting numeric types receive the correct type.
//
// Unset environment variables are left as-is so misconfiguration is
// visible in validation errors (e.g. "api.secret is required").
func expandEnvVars(data []byte) ([]byte, error) {
	// Fast path: skip if no placeholder pattern is present.
	if !strings.Contains(string(data), "${") {
		return data, nil
	}

	// Parse YAML into a generic tree. Placeholders are treated as plain
	// string scalars by the YAML parser — they cannot inject YAML
	// structure at this stage.
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse YAML for env var expansion: %w", err)
	}

	// Recursively expand env vars in all string leaf values, with type
	// inference to preserve YAML scalar types (int, float, bool).
	raw = expandEnvInTree(raw)

	// Re-marshal the expanded tree back to YAML bytes. The YAML encoder
	// properly quotes/escapes values containing special characters,
	// preventing injection.
	expanded, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to re-marshal YAML after env var expansion: %w", err)
	}

	return expanded, nil
}

// expandEnvInTree recursively walks a YAML-parsed value and expands
// ${ENV_VAR} placeholders in string leaf values. After expansion, the
// resulting string is passed through inferYamlScalar so that numeric
// and boolean values retain their YAML scalar type (e.g. "502" → int64
// 502), preserving the type-inference behaviour that byte-level
// substitution provided.
func expandEnvInTree(v any) any {
	switch val := v.(type) {
	case string:
		if !strings.Contains(val, "${") {
			return val
		}
		expanded := envVarRe.ReplaceAllStringFunc(val, func(match string) string {
			varName := match[2 : len(match)-1]
			if envVal, ok := os.LookupEnv(varName); ok {
				return envVal
			}
			return match // leave as-is if env var is not set
		})
		return inferYamlScalar(expanded)
	case map[string]any:
		for k, vv := range val {
			val[k] = expandEnvInTree(vv)
		}
		return val
	case []any:
		for i, vv := range val {
			val[i] = expandEnvInTree(vv)
		}
		return val
	default:
		return v
	}
}

// inferYamlScalar attempts to convert a string to its YAML scalar
// equivalent (int, float, bool, or nil) to preserve type inference
// after env var expansion. Strings that do not match any scalar type
// are returned unchanged.
func inferYamlScalar(s string) any {
	// Try integer (base 10).
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	// Try float.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	// Try bool (YAML 1.2 core schema: true/false in any case).
	switch s {
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	case "null", "Null", "NULL", "~":
		return nil
	}
	return s
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
// Callers that need to run the sentinel-merge before validating (the executor's
// Reload and dry-run Validate paths) use this, then call Validate explicitly
// after config.MergeSentinels restores real secret values.
//
// Splitting parse from validate is required because validate enforces
// api.secret minimum length, which rejects the "***" sentinel (3 chars) before
// the merge can restore the real value — see GET /configs/raw + PUT /configs
// round-trip in hub/executor.
func ParseNoValidate(data []byte) (*core.Config, error) {
	// Expand ${ENV_VAR} placeholders safely (parse → expand → re-marshal).
	expanded, err := expandEnvVars(data)
	if err != nil {
		return nil, err
	}

	cfg := &core.Config{}
	if err := yaml.Unmarshal(expanded, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	// Load tags from external files for drivers that use tags-file.
	if err := loadTagsFiles(cfg); err != nil {
		return nil, fmt.Errorf("failed to load tags files: %w", err)
	}
	return cfg, nil
}

// Validate runs the config validation rules against an already-parsed Config.
// Exported so the executor can validate AFTER the sentinel-merge restores real
// secret values (the unexported validate rejects the "***" sentinel for
// min-length secret fields).
func Validate(cfg *core.Config) error {
	return validate(cfg)
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

// validate performs config validation, failing fast on invalid values
// that would otherwise only surface at runtime.
func validate(cfg *core.Config) error {
	if err := validateBuffer(cfg); err != nil {
		return err
	}
	if err := validateDataSources(cfg); err != nil {
		return err
	}

	registeredDrivers := make(map[string]bool)
	for _, t := range core.RegisteredDrivers() {
		registeredDrivers[t] = true
	}
	registeredTransports := make(map[string]bool)
	for _, t := range core.RegisteredTransports() {
		registeredTransports[t] = true
	}

	if err := validateDrivers(cfg, registeredDrivers); err != nil {
		return err
	}
	if err := validateAPI(cfg); err != nil {
		return err
	}
	transportNames, err := validateTransports(cfg, registeredTransports)
	if err != nil {
		return err
	}
	if err := validateRules(cfg, transportNames); err != nil {
		return err
	}
	return nil
}

// validateBuffer checks offline buffer configuration.
func validateBuffer(cfg *core.Config) error {
	if cfg.Global.Buffer.Enabled {
		if cfg.Global.Buffer.Path == "" {
			return fmt.Errorf("buffer.enabled is true but buffer.path is not set")
		}
		if cfg.Global.Buffer.MaxSize > 0 && cfg.Global.Buffer.MaxSize < 10 {
			return fmt.Errorf("buffer.max-size must be at least 10, got %d", cfg.Global.Buffer.MaxSize)
		}
	}
	return nil
}

// validateDataSources ensures the config has at least one data source
// (driver, inbound transport, or auto-discovery) and at least one transport.
func validateDataSources(cfg *core.Config) error {
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
	return nil
}

// validateDrivers checks driver names are unique, types are registered,
// and tag fields are valid.
func validateDrivers(cfg *core.Config, registeredDrivers map[string]bool) error {
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
	return nil
}

// validateAPI checks that a shared secret is configured when the API
// server is enabled, and enforces minimum secret length.
func validateAPI(cfg *core.Config) error {
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
	return nil
}

// validateTransports checks transport names are unique, types are
// registered, and batch/retry fields are not misplaced inside settings.
// Returns the set of valid transport names for use by validateRules.
func validateTransports(cfg *core.Config, registeredTransports map[string]bool) (map[string]bool, error) {
	transportNames := make(map[string]bool)
	for _, t := range cfg.Transports {
		if t.Name == "" {
			return nil, fmt.Errorf("transport name cannot be empty")
		}
		if transportNames[t.Name] {
			return nil, fmt.Errorf("duplicate transport name: %s", t.Name)
		}
		transportNames[t.Name] = true
		if t.Type == "" {
			return nil, fmt.Errorf("transport %s: type cannot be empty", t.Name)
		}
		// M10: validate transport type against registry
		if len(registeredTransports) > 0 && !registeredTransports[t.Type] {
			return nil, fmt.Errorf("transport %s: unknown type %q (registered: %s)", t.Name, t.Type, strings.Join(core.RegisteredTransports(), ", "))
		}
		// Detect batch/retry fields misplaced inside the settings map.
		// These fields (batch-size, flush-interval, retry-count, buffer-size,
		// fallback) are top-level TransportConfig fields, siblings of
		// `settings`, not entries inside it. When written inside `settings`
		// they are silently ignored (stored as opaque map keys) and the
		// transport falls back to synchronous single-point publishing with
		// no error — a silent misconfiguration that is very hard to debug.
		// Fail fast so the user fixes the YAML indentation.
		for _, miskey := range []string{"batch-size", "batch_size", "flush-interval", "flush_interval", "retry-count", "retry_count", "buffer-size", "buffer_size", "fallback"} {
			if _, exists := t.Settings[miskey]; exists {
				return nil, fmt.Errorf("transport %s: field %q must be a top-level transport field (sibling of `settings`), not an entry inside `settings`; move it out one indentation level", t.Name, miskey)
			}
		}
	}
	return transportNames, nil
}

// validateRules checks rule names, actions, match expressions, and
// that rule targets reference existing transports.
func validateRules(cfg *core.Config, transportNames map[string]bool) error {
	// Valid rule actions for fail-fast validation (M8)
	validActions := map[string]bool{
		"forward": true, "drop": true, "alert": true,
		"transform": true, "mirror": true,
	}

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
