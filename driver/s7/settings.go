package s7

import (
	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// ValidateSettings implements core.SettingsValidator (audit CFG-4 /
// roadmap #25): rejects unknown keys so typos fail at config-validate
// time instead of silently running against the wrong PLC. The drift
// test (driver/settings_drift_test.go) fails when a key parsed by s7.go
// is missing here.
func (d *S7Driver) ValidateSettings(settings map[string]any) error {
	return util.RejectUnknownSettings(settings, map[string]bool{
		"host":                   true,
		"port":                   true,
		"rack":                   true,
		"slot":                   true,
		"timeout":                true,
		"idle-timeout":           true,
		"reconnect-interval":     true,
		"reconnect-max-interval": true,
		"max-reconnect-failures": true,
	}, "s7")
}

var _ core.SettingsValidator = (*S7Driver)(nil)
