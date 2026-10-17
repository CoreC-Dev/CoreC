package engine

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestApplyDeadbandFilterBadQualityPasses is the stage-B audit finding
// B5-2 regression test: a bad-quality point must penetrate the deadband
// (never swallowed) and must NOT update the baseline — otherwise the
// bad value (decoded as 0) poisons the baseline and subsequent GOOD
// values within the old→0 delta would be silently swallowed too.
func TestApplyDeadbandFilterBadQualityPasses(t *testing.T) {
	runner := &taskRunner{
		task: core.ScheduleTask{
			ID:        "t1",
			DeadBands: map[string]float64{"temp": 5.0},
		},
		lastValues: map[string]float64{"temp": 10.0}, // baseline 10
	}

	// 1. Bad-quality point (nil value): must pass through.
	values := []core.TagValue{
		{Tag: "temp", Value: nil, Quality: core.QualityBad},
	}
	got := applyDeadbandFilter(runner, values)
	if len(got) != 1 {
		t.Fatalf("bad-quality point swallowed by deadband: got %d values, want 1 (B5-2: quality degradation must be visible)", len(got))
	}

	// 2. Baseline must be untouched (not poisoned to 0).
	if v, ok := runner.lastValues["temp"]; !ok || v != 10.0 {
		t.Fatalf("baseline poisoned by bad-quality point: lastValues[%q] = %v (ok=%v), want 10.0", "temp", v, ok)
	}

	// 3. A subsequent GOOD value within the threshold of the ORIGINAL
	// baseline is still deadbanded (10 → 12, delta 2 < 5) — proving the
	// baseline was not reset to 0 (which would have swallowed nothing
	// here) nor jumped (which would have swallowed this).
	values = []core.TagValue{
		{Tag: "temp", Value: 12.0, Quality: core.QualityGood},
	}
	got = applyDeadbandFilter(runner, values)
	if len(got) != 0 {
		t.Fatalf("good value 12 vs baseline 10 (delta 2 < threshold 5) should be deadbanded, got %d values", len(got))
	}

	// 4. A GOOD value beyond the threshold passes and updates baseline.
	values = []core.TagValue{
		{Tag: "temp", Value: 20.0, Quality: core.QualityGood},
	}
	got = applyDeadbandFilter(runner, values)
	if len(got) != 1 {
		t.Fatalf("good value 20 vs baseline 10 (delta 10 >= threshold 5) should pass, got %d values", len(got))
	}
	if v := runner.lastValues["temp"]; v != 20.0 {
		t.Fatalf("baseline not updated after passing good value: got %v, want 20", v)
	}
}

// TestApplyDeadbandFilterUncertainQualityPasses covers the uncertain
// quality variant of the same rule.
func TestApplyDeadbandFilterUncertainQualityPasses(t *testing.T) {
	runner := &taskRunner{
		task: core.ScheduleTask{
			ID:        "t2",
			DeadBands: map[string]float64{"press": 2.0},
		},
		lastValues: map[string]float64{"press": 50.0},
	}
	values := []core.TagValue{
		{Tag: "press", Value: nil, Quality: core.QualityUncertain},
	}
	got := applyDeadbandFilter(runner, values)
	if len(got) != 1 {
		t.Fatalf("uncertain-quality point swallowed: got %d values, want 1", len(got))
	}
	if v := runner.lastValues["press"]; v != 50.0 {
		t.Fatalf("baseline changed by uncertain point: got %v, want 50", v)
	}
}
