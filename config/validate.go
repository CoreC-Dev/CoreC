package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// DriverRegistry provides access to the set of registered driver types.
// Implemented by core.Registry or test doubles — eliminates the config
// package's direct coupling to core.RegisteredDrivers() (ARCH-002).
type DriverRegistry interface {
	RegisteredDrivers() []string
}

// TransportRegistry provides access to the set of registered transport
// types. Implemented by core.Registry or test doubles — eliminates the
// config package's direct coupling to core.RegisteredTransports() (ARCH-002).
type TransportRegistry interface {
	RegisteredTransports() []string
}

// validate performs config validation, failing fast on invalid values
// that would otherwise only surface at runtime. The driver and transport
// registries are injected so the config package does not depend on
// global state (ARCH-002).
func validate(cfg *core.Config, drivers DriverRegistry, transports TransportRegistry) error {
	if err := validateBuffer(cfg); err != nil {
		return err
	}
	if err := validateDataSources(cfg); err != nil {
		return err
	}

	registeredDrivers := toSet(drivers.RegisteredDrivers())
	registeredTransports := toSet(transports.RegisteredTransports())

	// Type/shape checks first: they produce the most actionable
	// messages (e.g. the misplaced-batch-size hint names the exact
	// fix), and unknown keys inside a syntactically wrong map would
	// otherwise mask them with a generic allowlist error.
	if err := validateDrivers(cfg, registeredDrivers, drivers.RegisteredDrivers()); err != nil {
		return err
	}
	if err := validateAPI(cfg); err != nil {
		return err
	}
	transportNames, err := validateTransports(cfg, registeredTransports, transports.RegisteredTransports())
	if err != nil {
		return err
	}
	// Settings-key validation (audit CFG-4 / roadmap #25) runs after
	// the shape pass so its unknown-key errors are the FIRST message
	// only for genuinely unknown settings, not misplaced ones.
	if err := validateDriverSettings(cfg); err != nil {
		return err
	}
	if err := validateTransportSettings(cfg); err != nil {
		return err
	}
	if err := validateRules(cfg, transportNames); err != nil {
		return err
	}
	if err := validateDerivedTags(cfg); err != nil {
		return err
	}
	return nil
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
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

// validateDataSources is retained as a no-op for backward compatibility.
//
// Historically this enforced "at least one data source (driver, inbound
// transport, or auto-discovery) AND at least one transport" as a hard
// startup error. That blocked the dashboard-driven workflow where an
// operator starts CoreC with an empty/API-only config and builds the
// full configuration entirely through the Dashboard.
//
// The engine handles empty drivers/transports/rules safely (idle
// scheduler, vacuous readiness), so the presence requirement is now a
// non-blocking warning surfaced via IdleWarnings (logged at startup and
// on reload) rather than a validation failure. All other validations
// (unique names, registered types, valid tags, API secret, rule target
// refs, …) remain hard errors — only existence is relaxed, because an
// empty pipeline is a valid transient state in the Dashboard workflow.
func validateDataSources(cfg *core.Config) error {
	_ = cfg
	return nil
}

// IdleWarnings returns human-readable, non-blocking warnings for a
// config, surfaced through every existing warning channel: logged at
// startup (cmd/corec) and on reload (hub/executor), and returned by
// POST /configs/validate. Historically these covered only idle mode
// (no data source / no transport / no control plane); they now also
// cover configuration foot-guns that do not block startup but degrade
// data freshness — e.g. transport batching with no effective flush
// interval, whose trailing partial batch can then be held indefinitely
// (stage-B audit finding B1.1). Returns nil for a config with nothing
// to warn about.
func IdleWarnings(cfg *core.Config) []string {
	if cfg == nil {
		return nil
	}
	var warnings []string
	if cfg.Global.API.Listen == "" {
		warnings = append(warnings, "control plane (global.api.listen) not configured — Dashboard and HTTP API will not start; set global.api.listen and global.api.secret to enable remote configuration")
	}
	if len(cfg.Drivers) == 0 && !hasInboundTransport(cfg.Transports) && !hasAutoDiscoveryInbound(cfg) {
		warnings = append(warnings, "no data source configured — running in idle mode; add a driver, an inbound transport (mqtt data-topic / http webhook-addr), or enable auto-discovery with node.subscribe via the Dashboard")
	}
	if len(cfg.Transports) == 0 {
		warnings = append(warnings, "no transport configured — running in idle mode; add a northbound transport via the Dashboard")
	}
	warnings = append(warnings, batchingWarnings(cfg)...)
	return warnings
}

// batchingWarnings warns for every transport that would run a batcher
// with no effective flush interval while the offline buffer is
// disabled. In that combination the trailing partial batch can be held
// indefinitely — it is only sent when the batch fills or the process
// exits (stage-B audit finding B1.1: a low-rate tag with a large
// batch-size and no flush-interval can sit ~batch-size × tag-interval
// before being published; newTransportBatcher only defaults to a
// periodic flush when an offline buffer exists). A flush-interval that
// fails to parse as a positive duration is silently ignored by the
// batcher and therefore triggers the same warning.
func batchingWarnings(cfg *core.Config) []string {
	if cfg.Global.Buffer.Enabled {
		// With an offline buffer the batcher defaults to a 5s periodic
		// flush, so tail batches cannot be held indefinitely.
		return nil
	}
	var warnings []string
	for _, t := range cfg.Transports {
		// A batcher is created when any of batch-size / flush-interval /
		// retry-count is set (mirrors newTransportBatcher's nil check).
		if t.BatchSize <= 0 && t.FlushInterval == "" && t.RetryCount <= 0 {
			continue
		}
		if t.FlushInterval == "" {
			warnings = append(warnings, fmt.Sprintf(
				"transport %s: batching is enabled but no flush-interval is set and the offline buffer is disabled — the trailing partial batch may be held indefinitely (until the batch fills or CoreC stops); set flush-interval (e.g. \"5s\") to bound its latency", t.Name))
			continue
		}
		if d, err := time.ParseDuration(t.FlushInterval); err != nil || d <= 0 {
			warnings = append(warnings, fmt.Sprintf(
				"transport %s: flush-interval %q is not a positive duration and will be ignored — the trailing partial batch may be held indefinitely; use a value like \"5s\"", t.Name, t.FlushInterval))
		}
	}
	return warnings
}

// validateDrivers checks driver names are unique, types are registered,
// and tag fields are valid.
func validateDrivers(cfg *core.Config, registeredDrivers map[string]bool, allDriverTypes []string) error {
	driverNames := make(map[string]bool)
	for _, d := range cfg.Drivers {
		if err := validateDriver(d, driverNames, registeredDrivers, allDriverTypes); err != nil {
			return err
		}
	}
	return nil
}

// validateDriver validates a single driver configuration: unique name, non-empty
// type, registered type, at least one tag, and all tags valid.
func validateDriver(d core.DriverConfig, driverNames, registeredDrivers map[string]bool, allDriverTypes []string) error {
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
		return fmt.Errorf("driver %s: unknown type %q (registered: %s)", d.Name, d.Type, strings.Join(allDriverTypes, ", "))
	}
	if len(d.Tags) == 0 {
		return fmt.Errorf("driver %s: at least one tag must be configured", d.Name)
	}

	// M7: validate tag fields
	tagNames := make(map[string]bool)
	for _, tag := range d.Tags {
		if err := validateTag(d.Name, tag, tagNames); err != nil {
			return err
		}
	}
	return nil
}

// validateTag validates a single tag configuration: unique name, non-empty
// address and type, valid data type, and positive intervals/timeouts.
func validateTag(driverName string, tag core.TagConfig, tagNames map[string]bool) error {
	if tag.Name == "" {
		return fmt.Errorf("driver %s: tag name cannot be empty", driverName)
	}
	if tagNames[tag.Name] {
		return fmt.Errorf("driver %s: duplicate tag name %q", driverName, tag.Name)
	}
	tagNames[tag.Name] = true
	if tag.Address == "" {
		return fmt.Errorf("driver %s: tag %q: address cannot be empty", driverName, tag.Name)
	}
	if tag.Type == "" {
		return fmt.Errorf("driver %s: tag %q: type cannot be empty", driverName, tag.Name)
	}
	if _, ok := core.ParseDataType(tag.Type); !ok {
		return fmt.Errorf("driver %s: tag %q: invalid type %q (valid: bool, int8, int16, int32, int64, uint8, uint16, uint32, uint64, float32, float64, string, bytes)", driverName, tag.Name, tag.Type)
	}
	if err := validatePositiveDuration(driverName, tag.Name, "interval", tag.Interval); err != nil {
		return err
	}
	if err := validatePositiveDuration(driverName, tag.Name, "read-timeout", tag.ReadTimeout); err != nil {
		return err
	}
	return nil
}

// validatePositiveDuration validates that a duration string, if non-empty,
// parses to a positive duration.
func validatePositiveDuration(driverName, tagName, field, value string) error {
	if value == "" {
		return nil
	}
	dur, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("driver %s: tag %q: invalid %s %q: %w", driverName, tagName, field, value, err)
	}
	if dur <= 0 {
		return fmt.Errorf("driver %s: tag %q: %s must be positive, got %v", driverName, tagName, field, dur)
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
func validateTransports(cfg *core.Config, registeredTransports map[string]bool, allTransportTypes []string) (map[string]bool, error) {
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
			return nil, fmt.Errorf("transport %s: unknown type %q (registered: %s)", t.Name, t.Type, strings.Join(allTransportTypes, ", "))
		}
		// Detect batch/retry fields misplaced inside the settings map.
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
	validActions := map[string]bool{
		"forward": true, "drop": true, "alert": true,
		"transform": true, "mirror": true,
	}

	ruleNames := make(map[string]bool, len(cfg.Rules))
	for _, r := range cfg.Rules {
		if r.Name == "" {
			return fmt.Errorf("rule name cannot be empty")
		}
		// Rule names must be unique: PATCH /rules/disable and other
		// name-addressed operations rely on names as stable identifiers.
		if ruleNames[r.Name] {
			return fmt.Errorf("duplicate rule name: %s", r.Name)
		}
		ruleNames[r.Name] = true
		if r.Action == "" {
			return fmt.Errorf("rule %s: action cannot be empty", r.Name)
		}
		if !validActions[strings.ToLower(r.Action)] {
			return fmt.Errorf("rule %s: unknown action %q (valid: forward, drop, alert, transform, mirror)", r.Name, r.Action)
		}
		if r.Match == "" {
			return fmt.Errorf("rule %s: match cannot be empty", r.Name)
		}
		// drop + continue is contradictory: drop means "discard and stop",
		// continue means "keep evaluating" — the combination is nonsensical.
		if strings.EqualFold(r.Action, "drop") && r.Continue {
			return fmt.Errorf("rule %s: drop action cannot set continue: true", r.Name)
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
// or an HTTP transport with a webhook-addr.
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
// (node.id is set) and the node declares upstream subscriptions.
func hasAutoDiscoveryInbound(cfg *core.Config) bool {
	return cfg.Node.ID != "" && len(cfg.Node.Subscribe) > 0
}

// validateDerivedTags checks structural invariants for derived tag configs:
// unique names, non-empty expr and interval. Expression compilation and
// semantic validation happen in the engine at start time.
func validateDerivedTags(cfg *core.Config) error {
	names := make(map[string]bool)
	for _, dt := range cfg.DerivedTags {
		if strings.TrimSpace(dt.Name) == "" {
			return fmt.Errorf("derived tag has empty name")
		}
		if names[dt.Name] {
			return fmt.Errorf("duplicate derived tag name %q", dt.Name)
		}
		names[dt.Name] = true
		if strings.TrimSpace(dt.Expr) == "" {
			return fmt.Errorf("derived tag %q has empty expr", dt.Name)
		}
		if strings.TrimSpace(dt.Interval) == "" {
			return fmt.Errorf("derived tag %q has empty interval", dt.Name)
		}
	}
	return nil
}

// validateDriverSettings runs each driver's optional core.SettingsValidator
// (audit CFG-4 / roadmap #25): construct via the factory, type-assert the
// role, validate, discard. Construction is pure (no connection); the
// earlier validateDrivers pass already rejects unknown TYPES, so factory
// misses here are type-existence bugs, not user error. A driver that
// implements no validator keeps the historical behaviour (no extra
// checks), so the pass strictly widens validation coverage.
func validateDriverSettings(cfg *core.Config) error {
	for _, d := range cfg.Drivers {
		driver, err := core.CreateDriver(d)
		if err != nil {
			// Unknown type: reported by validateDrivers below with the
			// registered-type listing; skip here.
			continue
		}
		if v, ok := driver.(core.SettingsValidator); ok {
			if err := v.ValidateSettings(d.Settings); err != nil {
				return fmt.Errorf("driver %s: %w", d.Name, err)
			}
		}
	}
	return nil
}

// validateTransportSettings is the transport twin of validateDriverSettings.
func validateTransportSettings(cfg *core.Config) error {
	for _, t := range cfg.Transports {
		transport, err := core.CreateTransport(t)
		if err != nil {
			continue // unknown type: validateTransports reports it
		}
		if v, ok := transport.(core.SettingsValidator); ok {
			if err := v.ValidateSettings(t.Settings); err != nil {
				return fmt.Errorf("transport %s: %w", t.Name, err)
			}
		}
	}
	return nil
}
