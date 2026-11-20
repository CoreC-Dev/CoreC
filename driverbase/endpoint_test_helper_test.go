package driverbase

import "github.com/CoreC-Dev/CoreC/core"

func coreDriverConfig(typ string, settings map[string]any) core.DriverConfig {
	return core.DriverConfig{Name: "d", Type: typ, Settings: settings}
}
