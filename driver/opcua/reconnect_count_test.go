// Package opcua implements the OPC UA client driver for CoreC.
//
// This file holds tests for the ReconnectCount status field, which is
// populated from the reconnect loop's counted variant and surfaced via
// Status() so it can be exposed as a Prometheus metric.
package opcua

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestOPCUAReconnectCountStartsAtZero verifies that a freshly-constructed
// driver reports ReconnectCount == 0 via Status() before any reconnect
// attempt has been made.
func TestOPCUAReconnectCountStartsAtZero(t *testing.T) {
	drv, err := NewOPCUADriver(core.DriverConfig{Name: "rc-zero", Settings: map[string]any{}})
	if err != nil {
		t.Fatalf("NewOPCUADriver failed: %v", err)
	}

	st := drv.Status()
	if st.ReconnectCount != 0 {
		t.Errorf("expected ReconnectCount 0 on a fresh driver, got %d", st.ReconnectCount)
	}
}

// TestOPCUAReconnectCountIncrements verifies that ReconnectCount increases
// as the background reconnect loop makes attempts. The driver is pointed
// at opc.tcp://127.0.0.1:1 (a closed loopback port that refuses connections
// instantly) with a short timeout and short reconnect interval, so the
// loop retries quickly and ReconnectCount must grow monotonically.
//
// The test uses polling (not a fixed sleep) to observe the counter,
// matching the no-flaky-patterns rule in CONTRIBUTING.md. The reconnect
// goroutine is always torn down via t.Cleanup so the test never leaks a
// goroutine even if an assertion fails.
func TestOPCUAReconnectCountIncrements(t *testing.T) {
	cfg := core.DriverConfig{
		Name:     "rc-incr",
		Type:     "opcua",
		Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:1", "timeout": "300ms", "reconnect-interval": "30ms", "reconnect-max-interval": "60ms"},
		Tags:     []core.TagConfig{{Name: "x", Address: "ns=2;s=X", Type: "float64"}},
	}
	d := mustInit(t, cfg)
	// Always stop the background reconnect loop so the test never leaks a
	// goroutine, even if an assertion below fails.
	t.Cleanup(func() {
		if err := d.Stop(); err != nil {
			t.Errorf("cleanup Stop returned error: %v", err)
		}
	})

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	// Poll until the counter has advanced past zero (proving the counted
	// reconnect loop is wired up and is incrementing on each attempt).
	deadline := time.Now().Add(3 * time.Second)
	var first uint64
	for time.Now().Before(deadline) {
		first = d.Status().ReconnectCount
		if first > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first == 0 {
		t.Fatal("ReconnectCount never incremented; reconnect loop did not make any counted attempts")
	}

	// Wait a little longer and confirm the counter keeps growing — the
	// loop is still retrying against the closed port.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.Status().ReconnectCount > first {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := d.Status().ReconnectCount; got <= first {
		t.Errorf("expected ReconnectCount to keep growing, first=%d now=%d", first, got)
	}
}

// TestOPCUAReconnectCountPopulatedInStatus verifies that Status() returns a
// DriverStatus whose ReconnectCount field tracks the raw atomic counter on
// the driver struct. This guards the wiring between reconnectCount and
// Status() so the value is actually exposed to callers (and thus to the
// Prometheus metrics layer).
func TestOPCUAReconnectCountPopulatedInStatus(t *testing.T) {
	cfg := core.DriverConfig{
		Name:     "rc-status",
		Type:     "opcua",
		Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:1", "timeout": "300ms", "reconnect-interval": "30ms"},
		Tags:     []core.TagConfig{{Name: "x", Address: "ns=2;s=X", Type: "float64"}},
	}
	d := mustInit(t, cfg)
	t.Cleanup(func() {
		if err := d.Stop(); err != nil {
			t.Errorf("cleanup Stop returned error: %v", err)
		}
	})

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	// Wait for at least one counted reconnect attempt.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d.reconnectCount.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	raw := d.reconnectCount.Load()
	if raw == 0 {
		t.Fatal("expected the raw reconnectCount to have advanced past zero")
	}

	// Status() must mirror the raw counter value at the moment of the
	// snapshot. Because the loop is still running, capture both in quick
	// succession: load the atomic, then read Status() — Status() must be
	// >= that value (it can only have grown between the two reads).
	st := d.Status()
	if st.ReconnectCount < raw {
		t.Errorf("Status().ReconnectCount = %d, want >= raw counter %d (status must mirror the atomic)", st.ReconnectCount, raw)
	}
}
