// Package s7 implements the Siemens S7 protocol driver for CoreC.
// Supports S7-200, S7-300, S7-400, S7-1200, and S7-1500 PLCs.
//
// This file holds tests for the ReconnectCount status field, which is
// populated from the reconnect loop's counted variant and surfaced via
// Status() so it can be exposed as a Prometheus metric.
package s7

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/common/testutil"
	"github.com/CoreC-Dev/CoreC/core"
)

// TestS7ReconnectCountStartsAtZero verifies that a freshly-constructed
// driver reports ReconnectCount == 0 via Status() before any reconnect
// attempt has been made.
func TestS7ReconnectCountStartsAtZero(t *testing.T) {
	d := newTestDriver(t, "rc-zero")

	st := d.Status()
	if st.ReconnectCount != 0 {
		t.Errorf("expected ReconnectCount 0 on a fresh driver, got %d", st.ReconnectCount)
	}
}

// TestS7ReconnectCountIncrements verifies that ReconnectCount increases
// as the background reconnect loop makes attempts. The driver is pointed
// at 127.0.0.1:1 (a closed loopback port that refuses connections
// instantly) with a short reconnect interval, so the loop retries
// quickly and ReconnectCount must grow monotonically.
func TestS7ReconnectCountIncrements(t *testing.T) {
	d := newTestDriver(t, "rc-incr")

	cfg := core.DriverConfig{
		Name:     "rc-incr",
		Type:     "s7",
		Settings: map[string]any{"host": "127.0.0.1", "port": 1, "timeout": "200ms", "reconnect-interval": "30ms", "reconnect-max-interval": "60ms"},
		Tags:     []core.TagConfig{{Name: "x", Address: "MW0", Type: "uint16"}},
	}
	if err := d.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Stop(); err != nil {
			t.Errorf("cleanup Stop returned error: %v", err)
		}
	})

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	first := testutil.PollReconnectCount(t, d, 3*time.Second)
	testutil.PollReconnectCountGrowing(t, d, first, 2*time.Second)
}

// TestS7ReconnectCountPopulatedInStatus verifies that Status() returns a
// DriverStatus whose ReconnectCount field matches the raw atomic counter
// on the driver struct. This guards the wiring between reconnectCount
// and Status() so the value is actually exposed to callers (and thus to
// the Prometheus metrics layer).
func TestS7ReconnectCountPopulatedInStatus(t *testing.T) {
	d := newTestDriver(t, "rc-status")

	cfg := core.DriverConfig{
		Name:     "rc-status",
		Type:     "s7",
		Settings: map[string]any{"host": "127.0.0.1", "port": 1, "timeout": "200ms", "reconnect-interval": "30ms"},
		Tags:     []core.TagConfig{{Name: "x", Address: "MW0", Type: "uint16"}},
	}
	if err := d.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Stop(); err != nil {
			t.Errorf("cleanup Stop returned error: %v", err)
		}
	})

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	testutil.PollReconnectCount(t, d, 3*time.Second)

	raw := d.ReconnectCount()
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
