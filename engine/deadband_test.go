package engine

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestApplyDeadbandFilterBadQualityPasses is the regression test for
// bad-quality handling: a bad-quality point must penetrate the deadband
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

// TestApplyDeadbandFilterNonNumericPasses is the regression test for
// non-numeric tags: the documented behaviour says deadband only applies to
// numeric types, so bool/string/bytes tags must publish every sample —
// coercing them through ToFloat64 (bool→0/1, string→0) and comparing the
// fabricated zeros would freeze a string tag at its first sample forever.
func TestApplyDeadbandFilterNonNumericPasses(t *testing.T) {
	runner := &taskRunner{
		task: core.ScheduleTask{
			ID:   "t3",
			Tags: []string{},
			DeadBands: map[string]float64{
				"recipe":  5.0, // string tag
				"pumpOn":  5.0, // bool tag
				"rawBlob": 5.0, // bytes tag
				"level":   5.0, // numeric control
			},
		},
		lastValues: make(map[string]float64),
	}

	// First sample for each tag always passes (first-value rule).
	values := []core.TagValue{
		{Tag: "recipe", Value: "mixA", Quality: core.QualityGood},
		{Tag: "pumpOn", Value: true, Quality: core.QualityGood},
		{Tag: "rawBlob", Value: []byte{1, 2}, Quality: core.QualityGood},
		{Tag: "level", Value: 10.0, Quality: core.QualityGood},
	}
	if got := applyDeadbandFilter(runner, values); len(got) != 4 {
		t.Fatalf("first samples: got %d values, want 4 (first value always passes)", len(got))
	}

	// A CHANGED string value must pass too: the old implementation
	// compared ToFloat64("mixB")==0 against baseline 0 and swallowed it.
	values = []core.TagValue{
		{Tag: "recipe", Value: "mixB", Quality: core.QualityGood},
	}
	if got := applyDeadbandFilter(runner, values); len(got) != 1 {
		t.Fatalf("changed string value swallowed by deadband: got %d values, want 1 (B5-3: non-numeric tags must never freeze)", len(got))
	}

	// Bool transitions must pass on every sample.
	values = []core.TagValue{
		{Tag: "pumpOn", Value: false, Quality: core.QualityGood},
	}
	if got := applyDeadbandFilter(runner, values); len(got) != 1 {
		t.Fatalf("bool transition swallowed by deadband: got %d values, want 1", len(got))
	}

	// Bytes must pass on every sample.
	values = []core.TagValue{
		{Tag: "rawBlob", Value: []byte{9, 9, 9}, Quality: core.QualityGood},
	}
	if got := applyDeadbandFilter(runner, values); len(got) != 1 {
		t.Fatalf("bytes value swallowed by deadband: got %d values, want 1", len(got))
	}

	// Non-numeric values must never touch the baseline: only the numeric
	// tag may have an entry.
	for _, tag := range []string{"recipe", "pumpOn", "rawBlob"} {
		if _, ok := runner.lastValues[tag]; ok {
			t.Errorf("baseline updated for non-numeric tag %q: deadband must not record non-numeric baselines", tag)
		}
	}
	if v := runner.lastValues["level"]; v != 10.0 {
		t.Errorf("numeric baseline not recorded: lastValues[level] = %v, want 10", v)
	}

	// Numeric filtering is unchanged: a small delta is swallowed, a large
	// delta passes and updates the baseline.
	values = []core.TagValue{
		{Tag: "level", Value: 12.0, Quality: core.QualityGood},
	}
	if got := applyDeadbandFilter(runner, values); len(got) != 0 {
		t.Fatalf("numeric small delta should be deadbanded, got %d values", len(got))
	}
	values = []core.TagValue{
		{Tag: "level", Value: 20.0, Quality: core.QualityGood},
	}
	if got := applyDeadbandFilter(runner, values); len(got) != 1 {
		t.Fatalf("numeric large delta should pass, got %d values", len(got))
	}
	if v := runner.lastValues["level"]; v != 20.0 {
		t.Errorf("baseline not updated after passing numeric value: got %v, want 20", v)
	}
}
