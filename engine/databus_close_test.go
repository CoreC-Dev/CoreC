package engine

import (
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestDataBus_BroadcastAfterClose verifies that calling Broadcast after
// Close (or racing with Close) does not panic on a closed subscriber
// channel. Before the fix, Close did not clear the subscriber list or
// reset subCount, so a post-Close Broadcast would enter the send loop
// and hit `select { case sub.ch <- point: default: }` on a closed
// channel, which panics.
func TestDataBus_BroadcastAfterClose(t *testing.T) {
	bus := NewDataBus(16)
	_, unsub := bus.Subscribe("")
	_ = unsub // intentionally not unsubscribed; Close will close the channel

	bus.Close()

	// This must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Broadcast after Close panicked: %v", r)
		}
	}()
	bus.Broadcast(core.DataPoint{Driver: "d", Tag: "t", Value: 1.0, Timestamp: time.Now()})
}

// TestDataBus_BroadcastConcurrentWithClose verifies that Broadcast
// racing with Close does not panic. Multiple goroutines call Broadcast
// in a tight loop while another calls Close. Without the fix that clears
// subscribers and resets subCount in Close, this could panic on a
// closed-channel send.
func TestDataBus_BroadcastConcurrentWithClose(t *testing.T) {
	bus := NewDataBus(64)
	for i := 0; i < 10; i++ {
		_, _ = bus.Subscribe("")
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Broadcaster goroutines.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					func() {
						defer func() {
							if r := recover(); r != nil {
								t.Errorf("Broadcast panicked: %v", r)
							}
						}()
						bus.Broadcast(core.DataPoint{
							Driver:    "d",
							Tag:       "t",
							Value:     1.0,
							Timestamp: time.Now(),
						})
					}()
				}
			}
		}()
	}

	// Let broadcasters run briefly, then close.
	time.Sleep(10 * time.Millisecond)
	bus.Close()
	close(stop)
	wg.Wait()
}

// TestDataBus_CloseResetsSubCount verifies that Close resets subCount so
// the Broadcast fast path returns immediately after Close without
// acquiring the lock.
func TestDataBus_CloseResetsSubCount(t *testing.T) {
	bus := NewDataBus(8)
	_, _ = bus.Subscribe("")
	_, _ = bus.Subscribe("d1")

	if got := bus.subCount.Load(); got != 2 {
		t.Fatalf("subCount before Close = %d, want 2", got)
	}

	bus.Close()

	if got := bus.subCount.Load(); got != 0 {
		t.Fatalf("subCount after Close = %d, want 0", got)
	}
}
