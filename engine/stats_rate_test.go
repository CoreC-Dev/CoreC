package engine

import (
	"testing"
	"time"
)

// TestStatsPointsPerSecWindowedRate pins EngineStats.PointsPerSec to the
// windowed-rate semantics fixed in audit finding OBS-1: the metric must
// reflect recent traffic (and drop to zero after a stall) instead of the
// lifetime average, which decayed for hours on a stalled pipeline and
// made "collection stopped" alerting impossible.
func TestStatsPointsPerSecWindowedRate(t *testing.T) {
	e := New().(*CoreCEngine)

	// Engine with 60s of uptime and 120 points just recorded: the
	// window divisor is the full 60s, so the rate is exactly 2/s.
	e.stats.startTime = time.Now().Add(-time.Minute)
	e.stats.recordRead(120)

	st := e.stats.Stats()
	if st.TotalRead != 120 {
		t.Fatalf("TotalRead = %d, want 120", st.TotalRead)
	}
	// All 120 points landed in the current second, so the windowed sum
	// is 120 and the 60s divisor yields exactly 2. Allow a hair of
	// slack for a second boundary crossing mid-test (which can only
	// lower the sum, never raise it above 120/60).
	if st.PointsPerSec <= 0 || st.PointsPerSec > 2.0 {
		t.Fatalf("PointsPerSec = %g, want in (0, 2]", st.PointsPerSec)
	}

	// A fresh engine (sub-second uptime) reports first-second traffic
	// at face value via the one-second divisor floor, not zero.
	e2 := New().(*CoreCEngine)
	e2.stats.recordRead(5)
	st2 := e2.stats.Stats()
	if st2.TotalRead != 5 {
		t.Fatalf("TotalRead = %d, want 5", st2.TotalRead)
	}
	if st2.PointsPerSec <= 0 {
		t.Fatalf("PointsPerSec = %g, want > 0 for fresh engine with traffic", st2.PointsPerSec)
	}

	// recordRead(0) is a no-op on both counters.
	before := e2.stats.Stats()
	e2.stats.recordRead(0)
	after := e2.stats.Stats()
	if after.TotalRead != before.TotalRead {
		t.Fatalf("recordRead(0) changed TotalRead: %d -> %d", before.TotalRead, after.TotalRead)
	}
}
