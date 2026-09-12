// Package all imports all driver packages to register their factories.
package all

import (
	_ "github.com/CoreC-Dev/CoreC/driver/modbus"
	_ "github.com/CoreC-Dev/CoreC/driver/opcua"
	_ "github.com/CoreC-Dev/CoreC/driver/s7"
)
