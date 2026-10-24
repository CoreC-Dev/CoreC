package modbus

import (
	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// Settings validation: settings are map[string]any, so a typo like
// `slve-id` would otherwise sail through validation and silently run
// the driver against defaults. The known-key sets live
// next to the code that parses them; settings_drift_test.go fails when
// a parsed key is missing from a set.

// modbusSettingsKeys is the allowlist shared by every modbus variant;
// each variant's ValidateSettings unions it with its own keys.
func modbusSettingsKeys() map[string]bool {
	return map[string]bool{
		"slave-id":               true,
		"retry":                  true,
		"timeout":                true,
		"reconnect-interval":     true,
		"reconnect-max-interval": true,
		"max-reconnect-failures": true,
	}
}

// ValidateSettings implements core.SettingsValidator for the TCP/UDP
// variants (host/port).
func (d *ModbusTCPDriver) ValidateSettings(settings map[string]any) error {
	return util.RejectUnknownSettings(settings, union(modbusSettingsKeys(),
		map[string]bool{"host": true, "port": true}), "modbus")
}

// ValidateSettings implements core.SettingsValidator for the RTU
// variant (serial parameters).
func (d *ModbusRTUDriver) ValidateSettings(settings map[string]any) error {
	return util.RejectUnknownSettings(settings, union(modbusSettingsKeys(),
		map[string]bool{
			"serial-device": true,
			"baud-rate":     true,
			"data-bits":     true,
			"parity":        true,
			"stop-bits":     true,
		}), "modbus-rtu")
}

// ValidateSettings implements core.SettingsValidator for the TLS
// variant (host/port + certificate files).
func (d *ModbusTLSDriver) ValidateSettings(settings map[string]any) error {
	return util.RejectUnknownSettings(settings, union(modbusSettingsKeys(),
		map[string]bool{
			"host":      true,
			"port":      true,
			"cert-file": true,
			"key-file":  true,
			"ca-file":   true,
		}), "modbus-tls")
}

// ValidateSettings implements core.SettingsValidator for the
// rtuovertcp/udp/rtuoverudp network variants (host/port).
func (d *ModbusNetDriver) ValidateSettings(settings map[string]any) error {
	return util.RejectUnknownSettings(settings, union(modbusSettingsKeys(),
		map[string]bool{"host": true, "port": true}), "modbus")
}

// union merges two key sets (small maps; called once per validation).
func union(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

var _ core.SettingsValidator = (*ModbusNetDriver)(nil)
var _ core.SettingsValidator = (*ModbusTCPDriver)(nil)
var _ core.SettingsValidator = (*ModbusRTUDriver)(nil)
var _ core.SettingsValidator = (*ModbusTLSDriver)(nil)
