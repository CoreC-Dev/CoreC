package rule

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestContinueFlag verifies that rules with continue: true do not stop
// evaluation, allowing multiple rules to match a single data point.
func TestContinueFlag(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{
			Name:     "alert-high-temp",
			Match:    "tag == 'temperature' && value > 95",
			Action:   "alert",
			Target:   "alert-channel",
			Priority: 1,
			Continue: true, // alert then keep evaluating
		},
		{
			Name:     "default-forward",
			Match:    "ALL",
			Action:   "forward",
			Target:   "mes-http-push",
			Priority: 999,
			// Continue defaults to false → terminal
		},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// High temp: should match BOTH rules (alert with continue, then forward)
	results := eng.Match(core.DataPoint{Tag: "temperature", Value: 98.0})
	if len(results) != 2 {
		t.Fatalf("expected 2 matches for high temp, got %d", len(results))
	}
	if results[0].Rule.Name() != "alert-high-temp" {
		t.Errorf("expected first match alert-high-temp, got %s", results[0].Rule.Name())
	}
	if results[1].Rule.Name() != "default-forward" {
		t.Errorf("expected second match default-forward, got %s", results[1].Rule.Name())
	}

	// Normal temp: should match only the default (alert condition false)
	results = eng.Match(core.DataPoint{Tag: "temperature", Value: 50.0})
	if len(results) != 1 {
		t.Fatalf("expected 1 match for normal temp, got %d", len(results))
	}
	if results[0].Rule.Name() != "default-forward" {
		t.Errorf("expected default-forward, got %s", results[0].Rule.Name())
	}
}

// TestContinueBackwardCompat verifies that when no rule has continue: true,
// Match returns at most one result — identical to the former first-match-wins.
func TestContinueBackwardCompat(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "alert", Match: "value > 95", Action: "alert", Target: "ch", Priority: 1},
		{Name: "forward", Match: "ALL", Action: "forward", Target: "mes", Priority: 999},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// High value: only the alert rule matches (terminal, no continue)
	results := eng.Match(core.DataPoint{Tag: "t", Value: 100.0})
	if len(results) != 1 {
		t.Fatalf("expected 1 match (first-match-wins), got %d", len(results))
	}
	if results[0].Rule.Name() != "alert" {
		t.Errorf("expected alert, got %s", results[0].Rule.Name())
	}
}

// TestContinueMultipleNonTerminal verifies that several non-terminal rules
// can fire in sequence before a terminal rule stops evaluation.
func TestContinueMultipleNonTerminal(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "warn", Match: "value > 80", Action: "alert", Target: "ops", Priority: 10, Continue: true},
		{Name: "critical", Match: "value > 95", Action: "alert", Target: "safety", Priority: 11, Continue: true},
		{Name: "default", Match: "ALL", Action: "forward", Target: "mes", Priority: 999},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// value=98: warn hits, critical hits, default hits
	results := eng.Match(core.DataPoint{Tag: "t", Value: 98.0})
	if len(results) != 3 {
		t.Fatalf("expected 3 matches, got %d", len(results))
	}

	// value=85: warn hits, critical misses, default hits
	results = eng.Match(core.DataPoint{Tag: "t", Value: 85.0})
	if len(results) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(results))
	}
	if results[0].Rule.Name() != "warn" {
		t.Errorf("expected warn first, got %s", results[0].Rule.Name())
	}
	if results[1].Rule.Name() != "default" {
		t.Errorf("expected default second, got %s", results[1].Rule.Name())
	}

	// value=50: no alert matches, only default
	results = eng.Match(core.DataPoint{Tag: "t", Value: 50.0})
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
}

// TestContinueWithTransform verifies that a non-terminal transform rule
// modifies the point for subsequent rules.
func TestContinueWithTransform(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{
			Name:     "celsius-to-fahrenheit",
			Match:    "tag == 'temp_c'",
			Action:   "transform",
			Target:   "cloud",
			Priority: 10,
			Transform: &core.TransformConfig{
				Expression: "value * 9 / 5 + 32",
				TagRename:  "temp_f",
			},
			Continue: true,
		},
		{
			Name:     "default",
			Match:    "ALL",
			Action:   "forward",
			Target:   "mes",
			Priority: 999,
		},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// The transform rule matches and continues; the default rule also matches.
	results := eng.Match(core.DataPoint{Tag: "temp_c", Value: 100.0})
	if len(results) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(results))
	}
	if results[0].Rule.Name() != "celsius-to-fahrenheit" {
		t.Errorf("expected transform rule first, got %s", results[0].Rule.Name())
	}
	// The transform config should be carried in the first result.
	if results[0].Transform == nil {
		t.Error("expected non-nil transform in first result")
	}
}

// TestContinueStats verifies that hit/miss statistics are recorded correctly
// with continue rules.
func TestContinueStats(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "alert", Match: "value > 95", Action: "alert", Target: "ch", Priority: 1, Continue: true},
		{Name: "default", Match: "ALL", Action: "forward", Target: "mes", Priority: 999},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// High value: both rules hit
	eng.Match(core.DataPoint{Tag: "t", Value: 100.0})
	// Low value: alert misses, default hits
	eng.Match(core.DataPoint{Tag: "t", Value: 50.0})

	stats := eng.RuleStats()
	statMap := make(map[string]core.RuleStat, len(stats))
	for _, s := range stats {
		statMap[s.Name] = s
	}

	if statMap["alert"].HitCount != 1 {
		t.Errorf("alert hit count: expected 1, got %d", statMap["alert"].HitCount)
	}
	if statMap["alert"].MissCount != 1 {
		t.Errorf("alert miss count: expected 1, got %d", statMap["alert"].MissCount)
	}
	if statMap["default"].HitCount != 2 {
		t.Errorf("default hit count: expected 2, got %d", statMap["default"].HitCount)
	}
}

// TestContinueFlagInStats verifies that the Continue field is exposed in RuleStats.
func TestContinueFlagInStats(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "alert", Match: "ALL", Action: "alert", Target: "ch", Priority: 1, Continue: true},
		{Name: "forward", Match: "ALL", Action: "forward", Target: "mes", Priority: 2},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	stats := eng.RuleStats()
	if !stats[0].Continue {
		t.Error("expected rule 0 to have Continue=true")
	}
	if stats[1].Continue {
		t.Error("expected rule 1 to have Continue=false")
	}
}
