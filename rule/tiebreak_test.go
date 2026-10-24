// Tie-break tests for same-priority rule evaluation.
// Same-priority rules evaluate in configuration order —
// the stable sort keeps the config order within equal priorities and the
// driver-index merge uses the build position as the tie-break — so a
// universal rule no longer implicitly shadows a same-priority driver
// rule, and the relative order of same-priority rules is a documented
// contract instead of an accident of sort.Slice.
package rule

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// ruleStatsByName indexes the engine's rule statistics by rule name.
func ruleStatsByName(eng *Engine) map[string]core.RuleStat {
	stats := eng.RuleStats()
	m := make(map[string]core.RuleStat, len(stats))
	// Index rather than range-copy: core.RuleStat is a wide struct and
	// gocritic's rangeValCopy flags the per-iteration copy.
	for i := range stats {
		m[stats[i].Name] = stats[i]
	}
	return m
}

// TestMatchSamePriorityConfigOrderDriverFirst verifies that a driver rule
// listed before a same-priority universal rule is evaluated first and the
// universal rule is never reached when the driver rule terminates. Under
// the pre-B7-1 behaviour the universal rule always evaluated first on
// equal priority, systematically shadowing the driver rule.
func TestMatchSamePriorityConfigOrderDriverFirst(t *testing.T) {
	eng := NewEngine()
	configs := []core.RuleConfig{
		{Name: "driver-drop", Match: "driver == 'd1' && tag == 'x'", Action: "drop", Priority: 10},
		{Name: "universal-alert", Match: "ALL", Action: "alert", Priority: 10},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	res := firstMatch(eng.Match(core.DataPoint{Driver: "d1", Tag: "x", Value: 1.0}))
	if res == nil || res.Rule.Name() != "driver-drop" {
		t.Errorf("expected driver-drop (listed first in config), got %v", res)
	}

	// The universal rule sits after the terminal in config order: a
	// linear scan would not reach it, so it records neither hit nor miss.
	stats := ruleStatsByName(eng)
	if stats["universal-alert"].HitCount != 0 {
		t.Errorf("universal-alert hit count: got %d, want 0 (never evaluated)", stats["universal-alert"].HitCount)
	}
	if stats["universal-alert"].MissCount != 0 {
		t.Errorf("universal-alert miss count: got %d, want 0 (never reached)", stats["universal-alert"].MissCount)
	}
	if stats["driver-drop"].HitCount != 1 {
		t.Errorf("driver-drop hit count: got %d, want 1", stats["driver-drop"].HitCount)
	}
}

// TestMatchSamePriorityConfigOrderUniversalFirst verifies the symmetric
// case: a universal rule listed first in config still evaluates first —
// the tie-break is configuration order, not an implicit rule.
func TestMatchSamePriorityConfigOrderUniversalFirst(t *testing.T) {
	eng := NewEngine()
	configs := []core.RuleConfig{
		{Name: "universal-drop", Match: "ALL", Action: "drop", Priority: 10},
		{Name: "driver-alert", Match: "driver == 'd1' && tag == 'x'", Action: "alert", Priority: 10},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	res := firstMatch(eng.Match(core.DataPoint{Driver: "d1", Tag: "x", Value: 1.0}))
	if res == nil || res.Rule.Name() != "universal-drop" {
		t.Errorf("expected universal-drop (listed first in config), got %v", res)
	}

	stats := ruleStatsByName(eng)
	if stats["driver-alert"].HitCount != 0 || stats["driver-alert"].MissCount != 0 {
		t.Errorf("driver-alert must not be evaluated after a terminal that precedes it in config order: hits=%d misses=%d",
			stats["driver-alert"].HitCount, stats["driver-alert"].MissCount)
	}
}

// TestMatchSamePriorityWithinBucketConfigOrder verifies that two
// same-priority rules scoped to the same driver keep their configuration
// order — previously guaranteed only by accident of the sort algorithm.
func TestMatchSamePriorityWithinBucketConfigOrder(t *testing.T) {
	eng := NewEngine()
	configs := []core.RuleConfig{
		{Name: "first", Match: "driver == 'd1' && tag == 'x'", Action: "drop", Priority: 10},
		{Name: "second", Match: "driver == 'd1' && tag == 'x'", Action: "alert", Priority: 10},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	res := firstMatch(eng.Match(core.DataPoint{Driver: "d1", Tag: "x", Value: 1.0}))
	if res == nil || res.Rule.Name() != "first" {
		t.Errorf("expected 'first' (listed first in config), got %v", res)
	}
}

// TestSamePriorityMissAccountingFollowsConfigOrder verifies the miss
// statistics follow the same (priority, config-order) model: an
// other-driver rule that precedes the terminal match in config order
// would have been evaluated in a linear scan and records a miss, while
// one at or after the terminal position does not.
func TestSamePriorityMissAccountingFollowsConfigOrder(t *testing.T) {
	// Other-driver rule listed FIRST: it precedes the terminal in the
	// global order, so a linear scan reaches it → miss recorded.
	eng := NewEngine()
	configs := []core.RuleConfig{
		{Name: "other-driver", Match: "driver == 'd2' && tag == 'x'", Action: "alert", Priority: 10},
		{Name: "own-driver", Match: "driver == 'd1' && tag == 'x'", Action: "drop", Priority: 10},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}
	res := firstMatch(eng.Match(core.DataPoint{Driver: "d1", Tag: "x", Value: 1.0}))
	if res == nil || res.Rule.Name() != "own-driver" {
		t.Fatalf("expected own-driver, got %v", res)
	}
	stats := ruleStatsByName(eng)
	if stats["other-driver"].MissCount != 1 {
		t.Errorf("other-driver miss count: got %d, want 1 (preceded the terminal in config order)", stats["other-driver"].MissCount)
	}

	// Other-driver rule listed LAST: it sits at/after the terminal in the
	// global order, so a linear scan would not reach it → no miss.
	eng2 := NewEngine()
	configs2 := []core.RuleConfig{
		{Name: "own-driver", Match: "driver == 'd1' && tag == 'x'", Action: "drop", Priority: 10},
		{Name: "other-driver", Match: "driver == 'd2' && tag == 'x'", Action: "alert", Priority: 10},
	}
	if err := eng2.SetRules(configs2); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}
	res = firstMatch(eng2.Match(core.DataPoint{Driver: "d1", Tag: "x", Value: 1.0}))
	if res == nil || res.Rule.Name() != "own-driver" {
		t.Fatalf("expected own-driver, got %v", res)
	}
	stats = ruleStatsByName(eng2)
	if stats["other-driver"].MissCount != 0 {
		t.Errorf("other-driver miss count: got %d, want 0 (at/after the terminal in config order)", stats["other-driver"].MissCount)
	}
}
