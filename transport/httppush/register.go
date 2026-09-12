package httppush

import "github.com/CoreC-Dev/CoreC/core"

func init() {
	core.RegisterTransport("http", NewHTTPTransport)
}
