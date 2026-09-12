package engine

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestAddDriverDuplicateName exposes a bug where AddDriver silently
// overwrites an existing driver with the same name, leaking the old
// driver's goroutines and scheduler tasks.
func TestAddDriverDuplicateName(t *testing.T) {
	registerMockTypes()

	eng := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// Get the original driver instance.
	origDriver, _ := eng.GetDriver("dup")
	origMD := origDriver.(*mockDriverWithState)

	// Add a driver with the same name — should either error or
	// properly stop the old driver first.
	err := eng.AddDriver(core.DriverConfig{
		Name: "dup",
		Type: "mock-state",
		Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}},
	})

	// Check the old driver was stopped (not leaked).
	waitFor(2*time.Second, func() bool {
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

// TestAddTransportDuplicateName exposes the same bug for transports.
func TestAddTransportDuplicateName(t *testing.T) {
	registerMockTypes()

	eng := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// Add a transport with the same name.
	err := eng.AddTransport(core.TransportConfig{
		Name: "dup",
		Type: "mock-data",
	})

	// Either reject with error, or replace cleanly.
	// The key is: no panic, and the engine remains usable.
	if err != nil {
		// Error is acceptable (reject duplicate).
		return
	}

	// If it succeeded, verify the engine still works.
	stats := eng.Stats()
	if stats.Transports < 1 {
		t.Error("engine should still have at least 1 transport after duplicate add")
	}
}
