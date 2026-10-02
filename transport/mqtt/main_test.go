package mqtt

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all tests
// in the mqtt transport package. This catches issues like context
// cancellation not propagating to background goroutines (P1-2 fix).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// The paho MQTT client leaves background goroutines running
		// after Disconnect (connection retry, status changes). This is
		// a known behavior of paho.mqtt.golang v1.5.1 and not a leak
		// in our code. The %2e encoding is how Go's runtime represents
		// dots in module paths in stack traces.
		goleak.IgnoreAnyFunction("github.com/eclipse/paho.mqtt.golang.(*client).Disconnect.func1"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho.mqtt.golang.(*connectionStatus).Disconnecting"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho.mqtt.golang.(*client).Connect.func1"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho%2emqtt%2egolang.(*client).Disconnect.func1"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho%2emqtt%2egolang.(*connectionStatus).Disconnecting"),
		goleak.IgnoreAnyFunction("github.com/eclipse/paho%2emqtt%2egolang.(*client).Connect.func1"),
		// time.Sleep is used by paho's Connect.func1 for retry backoff.
		goleak.IgnoreAnyFunction("time.Sleep"),
	)
}
