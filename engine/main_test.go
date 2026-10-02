package engine

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all engine
// tests. The engine starts many background goroutines (drivers, batcher,
// transport, scheduler, tag watcher); goleak verifies they are all
// stopped when the engine is shut down.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// The Observable goroutine is started by the log package
		// initialization and runs for the process lifetime.
		goleak.IgnoreAnyFunction("github.com/CoreC-Dev/CoreC/common/observable.(*Observable[...]).start"),
		// The paho MQTT client leaves background goroutines after
		// Disconnect (known behavior of paho.mqtt.golang v1.5.1).
		goleak.IgnoreAnyFunction("github.com/eclipse/paho.mqtt.golang.(*client).Disconnect.func1"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho.mqtt.golang.(*connectionStatus).Disconnecting"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho.mqtt.golang.(*client).Connect.func1"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho%2emqtt%2egolang.(*client).Disconnect.func1"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho%2emqtt%2egolang.(*connectionStatus).Disconnecting"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho%2emqtt%2egolang.(*client).Connect.func1"),
		goleak.IgnoreAnyFunction("time.Sleep"),
	)
}
