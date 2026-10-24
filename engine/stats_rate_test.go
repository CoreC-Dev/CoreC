package engine

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestStatsPointsPerSecWindowedRate pins EngineStats.PointsPerSec to the
// windowed-rate semantics: the metric must
// reflect recent traffic (and drop to zero after a stall) instead of the
// lifetime average, which decays for hours on a stalled pipeline and
// makes "collection stopped" alerting impossible.
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

// TestBadQualityDropCounting pins the bad-quality split: the drop policy's
// discards increment the dedicated bad-quality counter and no longer
// pollute total_errors (a configured policy is not a fault).
func TestBadQualityDropCounting(t *testing.T) {
	e := New().(*CoreCEngine)
	e.processing.dataBus = NewDataBus(64)
	e.processing.badQualityPolicy = badQualityDrop

	for i := 0; i < 3; i++ {
		e.processing.processPoint(core.DataPoint{
			Driver: "d", Tag: "t", Value: 1.0, Quality: core.QualityBad,
			Timestamp: time.Now(),
		})
	}

	if got := e.DroppedBreakdown().BadQuality; got != 3 {
		t.Errorf("bad_quality dropped = %d, want 3", got)
	}
	if got := e.stats.totalErrors.Load(); got != 0 {
		t.Errorf("total_errors = %d, want 0 (drop policy is not an error)", got)
	}
}
