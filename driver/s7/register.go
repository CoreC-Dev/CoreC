package s7

import "github.com/CoreC-Dev/CoreC/core"

func init() {
	core.RegisterDriver("s7", NewS7Driver)
}
