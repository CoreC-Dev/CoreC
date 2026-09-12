package engine

import (
	"context"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			cfg := cfg1
			if i%2 == 1 {
				cfg = cfg2
			}
			_ = eng.Reload(cfg)
		}
	}()

	// Query goroutines.
	for q := 0; q < 4; q++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = eng.ListDrivers()
				_ = eng.ListTransports()
				_ = eng.Stats()
				_, _ = eng.GetDriver("d1")
				_, _ = eng.GetDriver("d2")
			}
		}()
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
