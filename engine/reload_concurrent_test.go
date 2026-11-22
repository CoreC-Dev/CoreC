package engine

import (
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestReloadConcurrentWithQueries verifies that Reload can be called
// concurrently with GetDriver/ListDrivers/Stats without deadlock or
// inconsistent state.
func TestReloadConcurrentWithQueries(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg1 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx := t.Context()

	if err := eng.Start(ctx, cfg1); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	cfg2 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d2", Type: "mock-state", Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	var wg sync.WaitGroup

	// Reload goroutine.
	wg.Go(func() {
		for i := range 10 {
			cfg := cfg1
			if i%2 == 1 {
				cfg = cfg2
			}
			_ = eng.Reload(cfg)
		}
	})

	// Query goroutines.
	for range 4 {
		wg.Go(func() {
			for range 100 {
				_ = eng.ListDrivers()
				_ = eng.ListTransports()
				_ = eng.Stats()
				_, _ = eng.GetDriver("d1")
				_, _ = eng.GetDriver("d2")
			}
		})
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(15 * time.Second):
		t.Fatal("deadlock: concurrent Reload + queries did not complete in 15s")
	}

	// Final state must be consistent: exactly 1 driver.
	drivers := eng.ListDrivers()
	if len(drivers) != 1 {
		t.Errorf("expected 1 driver after concurrent reload, got %d", len(drivers))
	}
}
