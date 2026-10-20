package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

var (
	errNotInitialized = errors.New("notify transport not initialized")
	errMissingSetting = errors.New("missing required setting")
)

// reqString extracts a required string setting.
func reqString(settings map[string]any, key string) (string, error) {
	v, ok := settings[key]
	if !ok {
		return "", fmt.Errorf("%w: %s", errMissingSetting, key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("setting %s must be a string", key)
	}
	return s, nil
}

// optString extracts an optional string setting, returning "" if absent.
func optString(settings map[string]any, key string) (string, error) {
	v, ok := settings[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("setting %s must be a string", key)
	}
	return s, nil
}

// optDuration extracts an optional duration setting (e.g. "5m").
func optDuration(settings map[string]any, key string) (time.Duration, error) {
	v, ok := settings[key]
	if !ok {
		return 0, nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("setting %s must be a duration string", key)
	}
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("setting %s: %w", key, err)
	}
	return d, nil
}

// optStringList extracts an optional list-of-strings setting. YAML lists
// decode as []any; each element must be a string. A bare comma-separated
// string is also accepted (e.g. "a@x.com,b@y.com") for form-friendly input.
func optStringList(settings map[string]any, key string) ([]string, error) {
	v, ok := settings[key]
	if !ok {
		return nil, nil
	}
	// Comma-separated string form (from Dashboard text field).
	if s, ok := v.(string); ok {
		parts := strings.Split(s, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("setting %s must be a list of strings or a comma-separated string", key)
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("setting %s[%d] must be a string", key, i)
		}
		out = append(out, s)
	}
	return out, nil
}

// optStringMap extracts an optional map[string]string setting. YAML maps
// decode as map[string]any; values must be strings. A JSON object string
// is also accepted (e.g. '{"Authorization":"Bearer ..."}') for form input.
func optStringMap(settings map[string]any, key string) (map[string]string, error) {
	v, ok := settings[key]
	if !ok {
		return map[string]string{}, nil
	}
	// JSON string form (from Dashboard text field).
	if s, ok := v.(string); ok {
		if s == "" {
			return map[string]string{}, nil
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, fmt.Errorf("setting %s must be a JSON object string: %w", key, err)
		}
		return m, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("setting %s must be a map of strings or a JSON object string", key)
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("setting %s[%s] must be a string", key, k)
		}
		out[k] = s
	}
	return out, nil
}

// optTemplate parses an optional text/template setting, falling back to def.
func optTemplate(settings map[string]any, key, def string) (*template.Template, error) {
	raw := def
	if v, ok := settings[key]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("setting %s must be a string", key)
		}
		if s != "" {
			raw = s
		}
	}
	return template.New(key).Parse(raw)
}

// ValidateSettings implements core.SettingsValidator (audit CFG-4 /
// roadmap #25): rejects unknown keys and checks the string-typed values
// at config-validate time (the full channel-specific parsing — required
// fields, durations, templates — still runs at Init). The drift test
// (transport/settings_drift_test.go) fails when a key parsed by the
// channel senders is missing here.
func (t *NotifyTransport) ValidateSettings(settings map[string]any) error {
	known := map[string]bool{
		"channel": true, "cooldown": true, "template": true,
		"webhook-url": true, "url": true, "method": true, "headers": true,
		"body-template": true,
		"smtp-host":     true, "smtp-port": true, "smtp-user": true, "smtp-pass": true,
		"from": true, "to": true, "subject": true, "body": true,
	}
	for key, val := range settings {
		if !known[key] {
			valid := make([]string, 0, len(known))
			for k := range known {
				valid = append(valid, k)
			}
			sort.Strings(valid)
			return fmt.Errorf("unknown notify setting %q (valid: %s)", key, strings.Join(valid, ", "))
		}
		// Strings except the two intentionally non-string fields.
		if key == "headers" || key == "to" {
			continue
		}
		if _, ok := val.(string); !ok {
			return fmt.Errorf("notify setting %q must be a string", key)
		}
	}
	return nil
}

var _ core.SettingsValidator = (*NotifyTransport)(nil)
