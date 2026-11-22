package engine

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/common/testutil"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestAddDriverDuplicateName verifies that AddDriver with an existing
// name either errors or stops the old driver first, leaving no leaked
// goroutines or scheduler tasks.
func TestAddDriverDuplicateName(t *testing.T) {
	registerMockTypes()

	eng := New()
	ctx := t.Context()

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "dup", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	origDriver, _ := eng.GetDriver("dup")
	origMD := origDriver.(*mockDriverWithState)

	err := eng.AddDriver(core.DriverConfig{
		Name: "dup",
		Type: "mock-state",
		Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}},
	})

	// Check the old driver was stopped (not leaked).
	testutil.WaitFor(2*time.Second, func() bool {
		origMD.mu.Lock()
		defer origMD.mu.Unlock()
		return origMD.stopCount > 0
	})
	origMD.mu.Lock()
	stopCount := origMD.stopCount
	origMD.mu.Unlock()

	if err == nil {
		// If AddDriver succeeded, the old driver must have been stopped.
		if stopCount == 0 {
			t.Error("BUG: AddDriver overwrote existing driver without stopping it — goroutine leak")
		}
	}
	// If AddDriver returned an error, that's also acceptable (reject duplicate).
}

// TestAddTransportDuplicateName verifies the same for transports.
func TestAddTransportDuplicateName(t *testing.T) {
	registerMockTypes()

	eng := New()
	ctx := t.Context()

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "dup", Type: "mock-data"}},
	}

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	orig, ok := eng.GetTransport("dup")
	if !ok {
		t.Fatal("original transport not found")
	}
	origMT := orig.(*mockTransportWithData)

	err := eng.AddTransport(core.TransportConfig{
		Name: "dup",
		Type: "mock-data",
	})

	// Either reject with error, or replace cleanly. A replacement that does
	// not stop the old transport leaks its listener goroutines and batcher.
	if err != nil {
		return
	}
	if !testutil.WaitFor(2*time.Second, func() bool {
		return origMT.stopCount.Load() > 0
	}) {
		t.Error("BUG: AddTransport replaced an existing transport without stopping it — goroutine leak")
	}
}
