package driverbase

import "github.com/CoreC-Dev/CoreC/core"

// coreDriverConfig is a tiny test helper for endpoint_test.go.
func coreDriverConfig(typ string, settings map[string]any) core.DriverConfig {
	return core.DriverConfig{Name: "d", Type: typ, Settings: settings}
}
