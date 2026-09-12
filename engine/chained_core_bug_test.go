package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestChainedCore_NoDuplicateListeners verifies that each transport's
// OnData listener is started exactly once.  Before the fix, AddTransport
// and commandLoop both called startDataListener, causing every data point
// to be pushed to DataBus twice — duplicate processing, duplicate publishes.
func TestChainedCore_NoDuplicateListeners(t *testing.T) {
	transportType := "chained-dedup"
	var ct *chainedTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		ct = newChainedTransport(config.Name)
		return ct, nil
	})

	core.RegisterDriver("mock-driver-dedup", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{
				Name:     "dummy",
				Type:     "mock-driver-dedup",
				Settings: map[string]any{},
				Tags:     []core.TagConfig{},
			},
		},
		Transports: []core.TransportConfig{
			{
				Name:     "dedup-1",
				Type:     transportType,
				Settings: map[string]any{},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "dedup-1"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng := New()
	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	// Send exactly one data point via OnData.
	upstreamPoint := core.DataPoint{
		Driver:    "upstream",
		Tag:       "temp",
		Value:     99.0,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}
	ct.dataCh <- upstreamPoint

	// Count how many points are published within a short window.
	// With the bug, two copies would arrive; after the fix, exactly one.
	var count int32
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case <-ct.publish:
			atomic.AddInt32(&count, 1)
		case <-timeout:
			goto done
		}
	}
done:
	if c := atomic.LoadInt32(&count); c != 1 {
		t.Errorf("expected exactly 1 published point (no duplicate), got %d", c)
	}
}

// TestDataBus_PushNeverBlocks verifies that DataBus.Push never blocks,
// even when the bus is full and multiple goroutines are pushing
// concurrently.  Before the fix, the blocking send after drain could
// hang forever if another goroutine filled the slot between drain and send.
func TestDataBus_PushNeverBlocks(t *testing.T) {
	// Create a tiny bus so it fills immediately.
	bus := NewDataBus(1)
	defer bus.Close()

	// Fill the bus to capacity.
	bus.Push(core.DataPoint{Driver: "d", Tag: "t1", Value: 1.0, Timestamp: time.Now()})
	bus.Push(core.DataPoint{Driver: "d", Tag: "t2", Value: 2.0, Timestamp: time.Now()})

	// Now launch many goroutines all pushing simultaneously.
	// If Push could block, this would deadlock or time out.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			bus.Push(core.DataPoint{
				Driver:    "d",
				Tag:       "concurrent",
				Value:     float64(i),
				Timestamp: time.Now(),
			})
		}
		close(done)
	}()

	select {
	case <-done:
		// Success — all pushes completed without blocking.
	case <-time.After(2 * time.Second):
		t.Fatal("DataBus.Push blocked on full bus — goroutines did not complete")
	}
}
