// Package util provides shared helper functions used across drivers,
// transports, and the rule engine.
//
// This file holds tests for ReconnectLoopWithBreakerCounted, the
// reconnect-loop variant that increments an external counter on every
// reconnect attempt so drivers can expose a reconnect Prometheus metric.
package util

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestReconnectLoopWithBreakerCounted_IncrementsOnSuccess verifies that
// the counter is incremented exactly once for a single, immediately
// successful connect attempt.
func TestReconnectLoopWithBreakerCounted_IncrementsOnSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	var counter atomic.Uint64
	connect := func() error {
		calls.Add(1)
		return nil
	}

	ReconnectLoopWithBreakerCounted(ctx, "test", connect, 5*time.Millisecond, 50*time.Millisecond, 0, &counter)

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 connect call, got %d", got)
	}
	if got := counter.Load(); got != 1 {
		t.Errorf("expected counter 1 after one successful attempt, got %d", got)
	}
}

// TestReconnectLoopWithBreakerCounted_IncrementsOnFailure verifies the
// counter is incremented for every reconnect attempt, including failed
// ones, and that the final (successful) attempt is also counted.
func TestReconnectLoopWithBreakerCounted_IncrementsOnFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	var counter atomic.Uint64
	connect := func() error {
		n := calls.Add(1)
		if n < 4 {
			return errors.New("connection refused")
		}
		return nil // success on the 4th attempt
	}

	ReconnectLoopWithBreakerCounted(ctx, "test", connect, 5*time.Millisecond, 50*time.Millisecond, 0, &counter)

	if got := calls.Load(); got != 4 {
		t.Fatalf("expected 4 connect calls, got %d", got)
	}
	// Counter must equal the number of attempts that began (3 failures + 1
	// success), regardless of outcome.
	if got := counter.Load(); got != 4 {
		t.Errorf("expected counter 4 after 4 attempts (3 fail + 1 success), got %d", got)
	}
}

// TestReconnectLoopWithBreakerCounted_NilCounterNoPanic verifies that a
// nil counter disables counting without breaking the reconnect loop
// (matching the behaviour of the plain ReconnectLoopWithBreaker wrapper).
func TestReconnectLoopWithBreakerCounted_NilCounterNoPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	connect := func() error {
		calls.Add(1)
		return nil
	}

	// Must not panic with a nil counter.
	ReconnectLoopWithBreakerCounted(ctx, "test", connect, 5*time.Millisecond, 50*time.Millisecond, 0, nil)

	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 connect call with nil counter, got %d", got)
	}
}

// TestReconnectLoopWithBreakerCounted_ContextCancelStops verifies that
// cancelling the context stops the loop. The counter must equal the
// number of attempts that actually started before cancellation, and the
// loop must not continue incrementing after the context is cancelled.
func TestReconnectLoopWithBreakerCounted_ContextCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	var counter atomic.Uint64
	connect := func() error {
		calls.Add(1)
		return errors.New("connection refused")
	}

	// Run the loop in a goroutine and cancel shortly after it begins.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ReconnectLoopWithBreakerCounted(ctx, "test", connect, 5*time.Millisecond, 20*time.Millisecond, 0, &counter)
	}()

	// Let a few attempts happen, then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	// After cancellation, the loop must have stopped: neither calls nor
	// counter should keep growing.
	callsAfterCancel := calls.Load()
	counterAfterCancel := counter.Load()

	time.Sleep(50 * time.Millisecond)

	if calls.Load() > callsAfterCancel+1 {
		t.Errorf("calls kept growing after cancel: %d -> %d", callsAfterCancel, calls.Load())
	}
	if counter.Load() > counterAfterCancel+1 {
		t.Errorf("counter kept growing after cancel: %d -> %d", counterAfterCancel, counter.Load())
	}
	// The counter and the call count must agree: every attempt that
	// started incremented both.
	if counter.Load() != uint64(calls.Load()) {
		t.Errorf("counter (%d) must equal calls (%d) — every started attempt is counted", counter.Load(), calls.Load())
	}
}

// TestReconnectLoopWithBreakerCounted_WrapperEquivalence verifies that
// the legacy ReconnectLoopWithBreaker wrapper (which passes a nil
// counter) and the counted variant with a nil counter behave the same
// way: both connect exactly once on immediate success.
func TestReconnectLoopWithBreakerCounted_WrapperEquivalence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Legacy wrapper.
	calls1 := 0
	ReconnectLoopWithBreaker(ctx, "legacy", func() error {
		calls1++
		return nil
	}, 5*time.Millisecond, 50*time.Millisecond, 0)

	// Counted variant with nil counter.
	calls2 := 0
	ReconnectLoopWithBreakerCounted(ctx, "counted", func() error {
		calls2++
		return nil
	}, 5*time.Millisecond, 50*time.Millisecond, 0, nil)

	if calls1 != 1 || calls2 != 1 {
		t.Errorf("expected both variants to call connect once, got legacy=%d counted=%d", calls1, calls2)
	}
}
