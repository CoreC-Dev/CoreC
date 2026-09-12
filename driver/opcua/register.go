package opcua

import "github.com/CoreC-Dev/CoreC/core"

func init() {
	core.RegisterDriver("opcua", NewOPCUADriver)
}
