package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestAddDriverConcurrentReplacement stress-tests the AddDriver fix
// under concurrent access. Multiple goroutines repeatedly add drivers
// with the same name while other goroutines call GetDriver/ListDrivers.
// This should not deadlock, panic, or leak goroutines.
func TestAddDriverConcurrentReplacement(t *testing.T) {
	registerMockTypes()

	eng := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "concurrent", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	const numWriters = 4
	const numReaders = 4
	const iterations = 20

	var wg sync.WaitGroup

	// Writers: repeatedly replace the same driver.
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = eng.AddDriver(core.DriverConfig{
					Name: "concurrent",
					Type: "mock-state",
					Tags: []core.TagConfig{{Name: fmt.Sprintf("t%d_%d", id, i), Interval: "100ms"}},
				})
			}
		}(w)
	}

	// Readers: repeatedly query the driver.
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if d, ok := eng.GetDriver("concurrent"); ok && d == nil {
					t.Error("GetDriver returned ok=true with nil driver")
				}
				_ = eng.ListDrivers()
			}
		}()
	}

	// Also test concurrent RemoveDriver + AddDriver.
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = eng.RemoveDriver("concurrent")
				_ = eng.AddDriver(core.DriverConfig{
					Name: "concurrent",
					Type: "mock-state",
					Tags: []core.TagConfig{{Name: fmt.Sprintf("rm_%d_%d", id, i), Interval: "100ms"}},
				})
			}
		}(w)
	}

	// Wait with timeout to detect deadlock.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success — no deadlock.
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: concurrent AddDriver/RemoveDriver did not complete in 10s")
	}

	// Final state should be consistent.
	drivers := eng.ListDrivers()
	if len(drivers) != 1 {
		t.Errorf("expected exactly 1 driver, got %d", len(drivers))
	}
}

// TestAddTransportConcurrentReplacement does the same for transports.
func TestAddTransportConcurrentReplacement(t *testing.T) {
	registerMockTypes()

	eng := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "concurrent", Type: "mock-data"}},
	}

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	const numWriters = 4
	const iterations = 20

	var wg sync.WaitGroup

	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = eng.AddTransport(core.TransportConfig{
					Name: "concurrent",
					Type: "mock-data",
				})
			}
		}(w)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: concurrent AddTransport did not complete in 10s")
	}

	transports := eng.ListTransports()
	if len(transports) != 1 {
		t.Errorf("expected exactly 1 transport, got %d", len(transports))
	}
}
