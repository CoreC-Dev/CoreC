package opcua

import (
	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// ValidateSettings implements core.SettingsValidator (audit CFG-4 /
// roadmap #25): rejects unknown keys so typos fail at config-validate
// time. The drift test (driver/settings_drift_test.go) fails when a key
// parsed by client.go is missing here.
func (d *OPCUADriver) ValidateSettings(settings map[string]any) error {
	return util.RejectUnknownSettings(settings, map[string]bool{
		"endpoint":               true,
		"security-policy":        true,
		"security-mode":          true,
		"username":               true,
		"password":               true,
		"timeout":                true,
		"cert-file":              true,
		"key-file":               true,
		"subscription-interval":  true,
		"subscription-buffer":    true,
		"max-batch-size":         true,
		"mode":                   true,
		"reconnect-interval":     true,
		"reconnect-max-interval": true,
		"max-reconnect-failures": true,
	}, "opcua")
}

var _ core.SettingsValidator = (*OPCUADriver)(nil)
