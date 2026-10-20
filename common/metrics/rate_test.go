package metrics

import (
	"sync"
	"testing"
	"time"
)

func at(sec int64) time.Time { return time.Unix(sec, 0) }

// TestRateWindowSteadyTraffic verifies the window sums a full minute of
// steady traffic and reports zero once the traffic stops for longer
// than the window (audit finding OBS-1: the lifetime-average
// points_per_second never dropped to zero when collection stalled).
func TestRateWindowSteadyTraffic(t *testing.T) {
	rw := NewRateWindow()
	for sec := int64(0); sec < 60; sec++ {
		rw.Observe(at(sec), 10)
	}
	if got := rw.Sum(at(59)); got != 600 {
		t.Errorf("steady minute: Sum = %d, want 600", got)
	}

	// Stall for a full window: every bucket is now stale.
	if got := rw.Sum(at(119)); got != 0 {
		t.Errorf("after 60s stall: Sum = %d, want 0", got)
	}
}

// TestRateWindowExcludesOldSeconds verifies traffic resumes correctly
// after a gap: stale buckets must reset, not accumulate into a burst.
func TestRateWindowExcludesOldSeconds(t *testing.T) {
	rw := NewRateWindow()
	rw.Observe(at(100), 5)
	// Seconds 100 and 160 share the same slot (100%60 == 160%60 == 40):
	// the new second's observation must reset the stale count, not add
	// to it.
	rw.Observe(at(160), 7)
	if got := rw.Sum(at(160)); got != 7 {
		t.Errorf("Sum after gap = %d, want 7 (stale second reset)", got)
	}
}

// TestRateWindowPartialWindow verifies only recent seconds are counted
// when the traffic is older than the window edge.
func TestRateWindowPartialWindow(t *testing.T) {
	rw := NewRateWindow()
	rw.Observe(at(1000), 1)
	rw.Observe(at(1001), 2)
	rw.Observe(at(1002), 3)
	if got := rw.Sum(at(1002)); got != 6 {
		t.Errorf("Sum = %d, want 6", got)
	}
	// Exactly at the window edge, second 1000 (now-60 exclusive bound
	// per the doc contract: stamps in (now-60, now]) is excluded.
	if got := rw.Sum(at(1060)); got != 5 {
		t.Errorf("edge Sum = %d, want 5 (only seconds 1001+1002)", got)
	}
	if got := rw.Sum(at(1063)); got != 0 {
		t.Errorf("beyond-window Sum = %d, want 0", got)
	}
}

// TestRateWindowSaturatesCount verifies the packed per-second count
// saturates at its bit width instead of corrupting the stamp.
func TestRateWindowSaturatesCount(t *testing.T) {
	rw := NewRateWindow()
	now := at(42)
	rw.Observe(now, rateCountMax)
	rw.Observe(now, rateCountMax) // would overflow the count field
	if got := rw.Sum(now); got != rateCountMax {
		t.Errorf("saturated Sum = %d, want %d", got, rateCountMax)
	}
}

// TestRateWindowConcurrentObserve hammers the same second's bucket from
// many goroutines; the -race detector and the final sum verify the
// CAS loop loses no increments (modulo saturation, which this test
// stays far below).
func TestRateWindowConcurrentObserve(t *testing.T) {
	rw := NewRateWindow()
	now := at(77)
	const goroutines, perGoroutine = 16, 1000
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				rw.Observe(now, 1)
			}
		}()
	}
	wg.Wait()
	want := uint64(goroutines * perGoroutine)
	if got := rw.Sum(now); got != want {
		t.Errorf("concurrent Sum = %d, want %d", got, want)
	}
}

// TestRateWindowZeroObserveIsNoOp verifies zero observations neither
// stamp nor increment buckets.
func TestRateWindowZeroObserveIsNoOp(t *testing.T) {
	rw := NewRateWindow()
	rw.Observe(at(10), 0)
	if got := rw.Sum(at(10)); got != 0 {
		t.Errorf("Sum after zero Observe = %d, want 0", got)
	}
}
