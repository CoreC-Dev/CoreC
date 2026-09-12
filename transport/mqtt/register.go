package mqtt

import "github.com/CoreC-Dev/CoreC/core"

func init() {
	core.RegisterTransport("mqtt", NewMQTTTransport)
}
