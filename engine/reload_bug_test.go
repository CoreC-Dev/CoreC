package engine

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestEngineReloadClearsOldDrivers exposes a bug where Reload() leaves
// old (stopped) drivers in the drivers map. After reload, ListDrivers()
// should only show the new config's drivers, not stale entries.
func TestEngineReloadClearsOldDrivers(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg1 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "old-driver", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg1); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Reload with a completely different driver.
	cfg2 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "new-driver", Type: "mock-state", Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// BUG: old-driver should NOT exist after reload.
	if _, ok := eng.GetDriver("old-driver"); ok {
		t.Error("BUG: old-driver still exists after Reload — stale entries not cleaned")
	}

	// new-driver should exist.
	if _, ok := eng.GetDriver("new-driver"); !ok {
		t.Error("new-driver should exist after Reload")
	}

	// ListDrivers should only show 1 driver, not 2.
	drivers := eng.ListDrivers()
	if len(drivers) != 1 {
		t.Errorf("expected 1 driver after reload, got %d: %+v", len(drivers), drivers)
	}
}

// TestEngineReloadClearsOldTransports exposes the same bug for transports.
func TestEngineReloadClearsOldTransports(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg1 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{
			{Name: "old-transport", Type: "mock-data"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg1); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	cfg2 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{
			{Name: "new-transport", Type: "mock-data"},
		},
	}

	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// BUG: old-transport should NOT exist after reload.
	if _, ok := eng.GetTransport("old-transport"); ok {
		t.Error("BUG: old-transport still exists after Reload — stale entries not cleaned")
	}

	// new-transport should exist.
	if _, ok := eng.GetTransport("new-transport"); !ok {
		t.Error("new-transport should exist after Reload")
	}

	transports := eng.ListTransports()
	if len(transports) != 1 {
		t.Errorf("expected 1 transport after reload, got %d: %+v", len(transports), transports)
	}
}

// TestEngineReloadStatsConsistency verifies that Stats() reflects the
// correct driver/transport count after reload, not accumulated counts.
func TestEngineReloadStatsConsistency(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg1 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
			{Name: "d2", Type: "mock-state", Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg1); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	cfg2 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d3", Type: "mock-state", Tags: []core.TagConfig{{Name: "t3", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Wait for reload to settle — poll until driver count stabilizes.
	waitFor(2*time.Second, func() bool {
		return eng.Stats().Drivers == 1
	})

	stats := eng.Stats()
	if stats.Drivers != 1 {
		t.Errorf("BUG: expected 1 driver in stats after reload, got %d (stale entries accumulated)", stats.Drivers)
	}
}
