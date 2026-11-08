// Package transport_test verifies that every settings key the transport
// code parses is accepted by the production ValidateSettings implementation
// of that transport. Checking the validator directly (rather than a second
// hand-maintained allowlist) catches both directions of drift: a new
// setting without a validator update, and a validator that rejects a
// setting the transport already accepts.
package transport_test

import (
	"os"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/common/testutil"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/transport/httppush"
	"github.com/CoreC-Dev/CoreC/transport/mqtt"
	"github.com/CoreC-Dev/CoreC/transport/notify"
)

var transportValidators = map[string]core.SettingsValidator{
	"mqtt":     &mqtt.MQTTTransport{},
	"httppush": &httppush.HTTPTransport{},
	"notify":   &notify.NotifyTransport{},
}

// sampleSettingValue supplies a value of the type the notify validator
// requires. mqtt/httppush only reject unknown keys, so any value works
// there; notify additionally type-checks every field except the two
// list/map fields.
func sampleSettingValue(key string) any {
	switch key {
	case "headers":
		return map[string]any{}
	case "to":
		return []any{}
	default:
		return ""
	}
}

func TestTransportSettingsAllowlistsCoverParsedKeys(t *testing.T) {
	for pkg, validator := range transportValidators {
		t.Run(pkg, func(t *testing.T) {
			// cwd is transport/.
			entries, err := os.ReadDir(pkg)
			if err != nil {
				t.Fatalf("read %s: %v", pkg, err)
			}
			parsed := make(map[string]bool)
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					continue
				}
				data, err := os.ReadFile(pkg + "/" + name)
				if err != nil {
					t.Fatalf("read file: %v", err)
				}
				testutil.CollectParsedSettingKeys(string(data), parsed)
			}
			for key := range parsed {
				if err := validator.ValidateSettings(map[string]any{key: sampleSettingValue(key)}); err != nil {
					t.Errorf("%s parses setting %q but its ValidateSettings rejects it: %v — update the transport's settings validator", pkg, key, err)
				}
			}
		})
	}
}
