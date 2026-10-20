package rule

import (
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestEvalErrorsCountedInRuleStat pins the eval-error visibility fix
// (audit findings PIPE-3/RULE-2): a match expression whose type does not
// fit the point (value > 90 against a string value) previously evaluated
// to a silent false — invisible in logs, stats, and preview. The rule
// must now report a growing eval_errors counter.
func TestEvalErrorsCountedInRuleStat(t *testing.T) {
	e := NewEngine()
	if err := e.SetRules([]core.RuleConfig{
		{Name: "mismatch", Match: "value > 90", Action: "drop"},
	}); err != nil {
		t.Fatalf("SetRules: %v", err)
	}

	// String values can never satisfy a numeric comparison: every
	// evaluation fails. The rule must count them, not silently miss.
	for i := 0; i < 3; i++ {
		e.Match(core.DataPoint{
			Driver: "d", Tag: "name", Value: "hello", Type: core.TypeString,
			Quality: core.QualityGood, Timestamp: time.Now(),
		})
	}

	stats := e.RuleStats()
	var mismatch *core.RuleStat
	for i := range stats {
		if stats[i].Name == "mismatch" {
			mismatch = &stats[i]
		}
	}
	if mismatch == nil {
		t.Fatal("mismatch rule missing from stats")
	}
	if mismatch.EvalErrors != 3 {
		t.Errorf("eval_errors = %d, want 3 (one per failing evaluation)", mismatch.EvalErrors)
	}
	if mismatch.EvalAt.IsZero() {
		t.Error("eval_at not set on evaluation failure")
	}
	if mismatch.HitCount != 0 {
		t.Errorf("hit_count = %d, want 0 (failed evaluations are not hits)", mismatch.HitCount)
	}
}

// TestPreviewMatchPropagatesEvalError verifies the preview path tells
// the rule author WHY the match failed instead of a bare false.
func TestPreviewMatchPropagatesEvalError(t *testing.T) {
	// Type mismatch: value > 90 against a string-valued point.
	point := core.DataPoint{
		Driver: "d", Tag: "name", Value: "hello", Type: core.TypeString,
		Quality: core.QualityGood, Timestamp: time.Now(),
	}
	matched, err := PreviewMatch("value > 90", point)
	if err == nil {
		t.Fatalf("expected evaluation error, got matched=%v nil error", matched)
	}
	if !strings.Contains(err.Error(), "expression evaluation failed") {
		t.Errorf("error = %v, want the evaluation-failure diagnosis", err)
	}

	// A well-typed expression still evaluates normally.
	okMatch, err := PreviewMatch("tag == 'name' && value == 'hello'", point)
	if err != nil || !okMatch {
		t.Errorf("valid match = %v, err = %v, want true/nil", okMatch, err)
	}

	// ALL and compile errors behave as before.
	allMatch, err := PreviewMatch("ALL", point)
	if err != nil || !allMatch {
		t.Errorf("ALL = %v, err = %v, want true/nil", allMatch, err)
	}
	if _, err := PreviewMatch("value >", point); err == nil {
		t.Error("expected compile error for incomplete expression")
	}
}

// TestTimestampInMatchExpression pins the roadmap #34 / RULE-3 fix:
// the point's Unix-millisecond timestamp is available to match
// expressions, so time-window rules (night-shift suppression, shift
// rollover) are expressible without external plumbing.
func TestTimestampInMatchExpression(t *testing.T) {
	// 2026-10-20T02:30:00Z — 02:30 UTC: within the 22:00–06:00 night window.
	night := core.DataPoint{
		Driver: "plc", Tag: "temp", Value: 95, Type: core.TypeFloat64,
		Quality: core.QualityGood, Timestamp: time.Date(2026, 10, 20, 2, 30, 0, 0, time.UTC),
	}
	noon := core.DataPoint{
		Driver: "plc", Tag: "temp", Value: 95, Type: core.TypeFloat64,
		Quality: core.QualityGood, Timestamp: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	}

	// Night window: hour >= 22 OR hour < 6 (UTC) in ms.
	nightExpr := "timestamp % 86400000 >= 79200000 || timestamp % 86400000 < 21600000"
	matched, err := PreviewMatch(nightExpr, night)
	if err != nil || !matched {
		t.Errorf("night point should match the night window, matched=%v err=%v", matched, err)
	}
	matched, err = PreviewMatch(nightExpr, noon)
	if err != nil || matched {
		t.Errorf("noon point should NOT match the night window, matched=%v err=%v", matched, err)
	}
}
