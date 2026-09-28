// Package config: secret redaction and sentinel-merge for safe config round-trips.
//
// CoreC exposes the full active configuration via GET /configs/raw so the
// Dashboard's Config Center can populate its editor with the server's real
// config. That config contains credentials (api.secret, mqtt passwords,
// http webhook-secrets, auth headers, …) which must never leak to the
// operator's browser. Redact replaces every secret VALUE with the SentinelValue
// ("***") while preserving keys and structure, so the operator sees where
// secrets are configured without seeing them.
//
// The inverse problem: when the operator submits the (possibly edited) config
// back via PUT /configs, any unchanged secret still carries "***". A naive full
// reload would persist the literal "***", breaking credentials. MergeSentinels
// runs in the executor's Reload path before ApplyConfig: for every secret whose
// incoming value is exactly SentinelValue, it backfills the real value from the
// currently active config. Secrets the operator deliberately changed (any value
// other than "***") pass through untouched.
package config

import (
	"fmt"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/goccy/go-yaml"
)

// SentinelValue is the placeholder substituted for secret values in the
// redacted config. The executor's MergeSentinels treats any secret field
// whose incoming value equals SentinelValue as "unchanged — backfill the
// live value" during a config reload.
const SentinelValue = "***"

// secretSettingKeys are keys within a driver/transport `settings` map whose
// values are credentials and must be redacted. These cover the secrets found
// across all bundled transports and drivers:
//   - mqtt:    username, password, command-secret, command-forward-secret
//   - http:    webhook-secret
//   - drivers: username, password (modbus/opcua/s7 auth)
//
// TLS *file paths* (tls-ca-file, tls-cert-file, tls-key-file, ca-file,
// cert-file, key-file) are intentionally EXCLUDED: they reference filesystem
// locations, not secret material, and redacting them would only add noise
// (the merge would then backfill a path, which is pointless).
var secretSettingKeys = map[string]bool{
	"username":               true,
	"password":               true,
	"command-secret":         true,
	"command-forward-secret": true,
	"webhook-secret":         true,
}

// headersKey is the nested map inside an HTTP transport's settings that
// carries outbound HTTP headers. Header values frequently carry credentials
// (Authorization, X-API-Key, …), so every value is redacted while the keys
// are preserved so the operator can still see which headers are configured.
const headersKey = "headers"

// IsSecretSettingKey reports whether a key names a secret field inside a
// driver/transport settings map. Exposed for tests and red-team verification.
func IsSecretSettingKey(key string) bool {
	return secretSettingKeys[key]
}

// deepCopyConfig returns a value-independent copy of cfg via a YAML
// marshal/unmarshal round-trip. The redacted copy is only ever serialized back
// to YAML for the HTTP response, so any type normalization (e.g. int → int64)
// is irrelevant — the on-the-wire representation is identical. The original
// cfg (the live currentCfg) is never mutated.
func deepCopyConfig(cfg *core.Config) (*core.Config, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("redact: failed to marshal config for deep copy: %w", err)
	}
	cp := &core.Config{}
	if err := yaml.Unmarshal(data, cp); err != nil {
		return nil, fmt.Errorf("redact: failed to unmarshal config copy: %w", err)
	}
	return cp, nil
}

// Redact returns a deep copy of cfg with every secret value replaced by
// SentinelValue. The original cfg is never mutated.
//
// Semantics a red-team audit must verify:
//  1. A non-empty secret → "***" (presence is visible, value is not).
//  2. An empty/absent secret stays empty/absent (no fake "***" injected, so
//     the merge can distinguish "secret configured" from "no secret").
//  3. Non-secret fields (broker, host, topic, port, …) are untouched.
//  4. The `headers` nested map keeps its keys but every non-empty value → "***".
func Redact(cfg *core.Config) (*core.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	cp, err := deepCopyConfig(cfg)
	if err != nil {
		return nil, err
	}

	// api.secret (struct field, not a settings-map key)
	if cp.Global.API.Secret != "" {
		cp.Global.API.Secret = SentinelValue
	}

	for i := range cp.Transports {
		redactSettings(cp.Transports[i].Settings)
	}
	for i := range cp.Drivers {
		redactSettings(cp.Drivers[i].Settings)
	}
	return cp, nil
}

// redactSettings replaces every non-empty secret value in a settings map with
// SentinelValue. The `headers` nested map is walked so each header VALUE is
// redacted while keys are preserved.
func redactSettings(settings map[string]any) {
	for k, v := range settings {
		if secretSettingKeys[k] {
			if s, ok := v.(string); ok && s != "" {
				settings[k] = SentinelValue
			}
			continue
		}
		if k == headersKey {
			if hdrs, ok := v.(map[string]any); ok {
				for hk, hv := range hdrs {
					if s, ok := hv.(string); ok && s != "" {
						hdrs[hk] = SentinelValue
					}
				}
			}
		}
	}
}

// MergeSentinels replaces every secret value in `incoming` that equals
// SentinelValue with the corresponding value from `current` (the live config).
// This allows a redacted config obtained from GET /configs/raw to be round-
// tripped through PUT /configs without losing credentials: the operator sees
// "***" for secrets they did not change, and the merge restores the real
// values before ApplyConfig persists the config.
//
// Rules:
//   - incoming secret == "***" AND current has that secret      → backfill real value
//   - incoming secret == "***" AND current has NO such secret   → drop the key
//     (no live credential existed; persisting "***" would be wrong, and an empty
//     value makes any required-credential validation fail loudly instead)
//   - incoming secret != "***" (incl. empty / new value)        → untouched
//     (operator deliberately changed or cleared it)
//
// `incoming` is mutated in place; it is a freshly parsed config not yet applied,
// so mutation is safe. `current` is never mutated.
func MergeSentinels(incoming, current *core.Config) {
	if incoming == nil || current == nil {
		return
	}

	// api.secret
	if incoming.Global.API.Secret == SentinelValue {
		incoming.Global.API.Secret = current.Global.API.Secret
	}

	for i := range incoming.Transports {
		incoming.Transports[i].Settings = mergeSettings(
			incoming.Transports[i].Settings,
			lookupTransportSettings(current.Transports, incoming.Transports[i].Name),
		)
	}
	for i := range incoming.Drivers {
		incoming.Drivers[i].Settings = mergeSettings(
			incoming.Drivers[i].Settings,
			lookupDriverSettings(current.Drivers, incoming.Drivers[i].Name),
		)
	}
}

// mergeSettings backfills "***" secret values from current into incoming.
// Returns the (possibly mutated) incoming map. If incoming is nil, returns nil.
func mergeSettings(incoming, current map[string]any) map[string]any {
	if incoming == nil {
		return nil
	}
	for k, v := range incoming {
		if secretSettingKeys[k] {
			if s, ok := v.(string); ok && s == SentinelValue {
				if current != nil {
					if cv, exists := current[k]; exists {
						incoming[k] = cv
					} else {
						delete(incoming, k)
					}
				} else {
					delete(incoming, k)
				}
			}
			continue
		}
		if k == headersKey {
			if hdrs, ok := v.(map[string]any); ok {
				var curHdrs map[string]any
				if current != nil {
					if ch, ok := current[headersKey].(map[string]any); ok {
						curHdrs = ch
					}
				}
				for hk, hv := range hdrs {
					if s, ok := hv.(string); ok && s == SentinelValue {
						if cv, exists := curHdrs[hk]; exists {
							hdrs[hk] = cv
						} else {
							delete(hdrs, hk)
						}
					}
				}
			}
		}
	}
	return incoming
}

// lookupTransportSettings returns the settings map of the transport named
// `name` in the current config, or nil if no such transport exists (e.g. the
// incoming config adds a brand-new transport that has no live counterpart).
func lookupTransportSettings(transports []core.TransportConfig, name string) map[string]any {
	for _, t := range transports {
		if t.Name == name {
			return t.Settings
		}
	}
	return nil
}

// lookupDriverSettings returns the settings map of the driver named `name` in
// the current config, or nil if no such driver exists.
func lookupDriverSettings(drivers []core.DriverConfig, name string) map[string]any {
	for _, d := range drivers {
		if d.Name == name {
			return d.Settings
		}
	}
	return nil
}
