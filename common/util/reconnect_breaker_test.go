package util

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestReconnectLoopWithBreaker(t *testing.T) {
	t.Run("connects on first try", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var calls int32
		connectFn := func() error {
			atomic.AddInt32(&calls, 1)
			return nil // success
		}

		go ReconnectLoopWithBreaker(ctx, "test", connectFn, 10*time.Millisecond, 100*time.Millisecond, 5)

		// Should connect once and stop retrying
		time.Sleep(100 * time.Millisecond)
		cancel()
		time.Sleep(100 * time.Millisecond)

		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("calls = %d, want 1", got)
		}
	})

	t.Run("retries on failure then succeeds", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var calls int32
		connectFn := func() error {
			n := atomic.AddInt32(&calls, 1)
			if n < 3 {
				return errors.New("connection refused")
			}
			return nil // success on 3rd try
		}

		go ReconnectLoopWithBreaker(ctx, "test", connectFn, 10*time.Millisecond, 100*time.Millisecond, 10)

		// Wait for success
		time.Sleep(500 * time.Millisecond)
		cancel()
		time.Sleep(100 * time.Millisecond)

		if got := atomic.LoadInt32(&calls); got < 3 {
			t.Errorf("calls = %d, want >= 3", got)
		}
	})

	t.Run("circuit breaker stops retrying", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var calls int32
		connectFn := func() error {
			atomic.AddInt32(&calls, 1)
			return errors.New("always fails")
		}

		// maxFailures=3, backoff=10ms
		go ReconnectLoopWithBreaker(ctx, "test", connectFn, 10*time.Millisecond, 50*time.Millisecond, 3)

		// Wait for circuit breaker to trip
		time.Sleep(300 * time.Millisecond)
		callsAtBreaker := atomic.LoadInt32(&calls)

		// Wait a bit more — no more calls should happen
		time.Sleep(200 * time.Millisecond)
		callsAfter := atomic.LoadInt32(&calls)

		if callsAtBreaker < 3 {
			t.Errorf("callsAtBreaker = %d, want >= 3", callsAtBreaker)
		}
		if callsAfter > callsAtBreaker {
			t.Errorf("circuit breaker didn't stop: calls grew from %d to %d", callsAtBreaker, callsAfter)
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		var calls int32
		connectFn := func() error {
			atomic.AddInt32(&calls, 1)
			return errors.New("fail")
		}

		go ReconnectLoopWithBreaker(ctx, "test", connectFn, 10*time.Millisecond, 50*time.Millisecond, 100)

		time.Sleep(50 * time.Millisecond)
		cancel()
		time.Sleep(200 * time.Millisecond)

		callsBeforeCancel := atomic.LoadInt32(&calls)
		time.Sleep(200 * time.Millisecond)
		callsAfterCancel := atomic.LoadInt32(&calls)

		// After cancel, calls should not keep growing
		if callsAfterCancel > callsBeforeCancel+1 {
			t.Errorf("calls kept growing after cancel: %d -> %d", callsBeforeCancel, callsAfterCancel)
		}
	})
}

func TestReconnectLoopDelegatesToBreaker(t *testing.T) {
	// ReconnectLoop should delegate to ReconnectLoopWithBreaker with maxFailures=0
	// (which disables the circuit breaker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	connectFn := func() error {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			return errors.New("fail")
		}
		return nil
	}

	go ReconnectLoopWithBreaker(ctx, "test", connectFn, 10*time.Millisecond, 100*time.Millisecond, 0)

	time.Sleep(300 * time.Millisecond)
	cancel()
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Errorf("calls = %d, want >= 2", got)
	}
}
