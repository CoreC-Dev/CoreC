package rule

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// firstMatch extracts the first result from a Match call, returning nil
// if no rule matched. This preserves the pre-continue-flag test idiom
// where Match returned a single *MatchResult.
func firstMatch(res []*MatchResult) *MatchResult {
	if len(res) == 0 {
		return nil
	}
	return res[0]
}

func TestEngineMatch(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{
			Name:     "high-temp-alert",
			Match:    "tag == 'temperature' && value > 50",
			Action:   "alert",
			Target:   "mqtt-alerts",
			Priority: 10,
		},
		{
			Name:     "default-route",
			Match:    "ALL",
			Action:   "forward",
			Target:   "mqtt-default",
			Priority: 100,
		},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("failed to set rules: %v", err)
	}

	// Case 1: matches high temp alert
	p1 := core.DataPoint{
		Driver: "modbus1",
		Tag:    "temperature",
		Value:  65.5,
	}
	res1 := firstMatch(eng.Match(p1))
	if res1 == nil {
		t.Fatalf("expected match for p1, got nil")
	}
	if res1.Rule.Name() != "high-temp-alert" {
		t.Errorf("expected rule high-temp-alert, got %s", res1.Rule.Name())
	}
	if len(res1.Targets) != 1 || res1.Targets[0] != "mqtt-alerts" {
		t.Errorf("expected targets [mqtt-alerts], got %v", res1.Targets)
	}

	// Case 2: normal temp matches default-route
	p2 := core.DataPoint{
		Driver: "modbus1",
		Tag:    "temperature",
		Value:  25.0,
	}
	res2 := firstMatch(eng.Match(p2))
	if res2 == nil {
		t.Fatalf("expected match for p2, got nil")
	}
	if res2.Rule.Name() != "default-route" {
		t.Errorf("expected rule default-route, got %s", res2.Rule.Name())
	}
	if len(res2.Targets) != 1 || res2.Targets[0] != "mqtt-default" {
		t.Errorf("expected targets [mqtt-default], got %v", res2.Targets)
	}
}

func TestEvalSimpleConditionTypes(t *testing.T) {
	p := core.DataPoint{
		Driver:  "d1",
		Device:  "dev1",
		Group:   "g1",
		Tag:     "t1",
		Type:    core.TypeFloat64,
		Quality: core.QualityGood,
		Value:   int32(42),
	}

	check := func(expr string, expect bool) {
		t.Helper()
		node, err := compileExpr(expr)
		if err != nil {
			t.Fatalf("compile %q failed: %v", expr, err)
		}
		got := node.eval(p)
		if got != expect {
			t.Errorf("expr %q: expected %v, got %v", expr, expect, got)
		}
	}

	check("driver == 'd1'", true)
	check("device == 'dev1'", true)
	check("group == 'g1'", true)
	check("tag == 't1'", true)
	check("type == 'float64'", true)
	check("quality == 'good'", true)
	check("value >= 40", true)
	check("value <= 42", true)
	check("value > 50", false)
}

func TestRuleStats(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "rule-a", Match: "tag == 'temp'", Action: "forward", Target: "mqtt1", Priority: 10},
		{Name: "rule-b", Match: "ALL", Action: "forward", Target: "mqtt2", Priority: 100},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// Match: rule-a hits, rule-b hits
	firstMatch(eng.Match(core.DataPoint{Tag: "temp", Value: 25.0}))
	// Match: rule-a misses, rule-b hits
	firstMatch(eng.Match(core.DataPoint{Tag: "pressure", Value: 30.0}))

	stats := eng.RuleStats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 rule stats, got %d", len(stats))
	}

	if stats[0].HitCount != 1 {
		t.Errorf("rule-a hit count: expected 1, got %d", stats[0].HitCount)
	}
	if stats[0].MissCount != 1 {
		t.Errorf("rule-a miss count: expected 1, got %d", stats[0].MissCount)
	}
	if stats[1].HitCount != 1 {
		t.Errorf("rule-b hit count: expected 1, got %d", stats[1].HitCount)
	}
	if stats[1].Name != "rule-b" {
		t.Errorf("expected rule-b, got %s", stats[1].Name)
	}
	if stats[1].Action != "forward" {
		t.Errorf("expected action forward, got %s", stats[1].Action)
	}
}

func TestRuleDisable(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "rule-a", Match: "tag == 'temp'", Action: "forward", Target: "mqtt1", Priority: 10},
		{Name: "rule-b", Match: "ALL", Action: "forward", Target: "mqtt2", Priority: 100},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// Disable rule-a
	if err := eng.SetRuleDisabled(0, true); err != nil {
		t.Fatalf("SetRuleDisabled failed: %v", err)
	}

	// rule-a should be skipped, rule-b should match
	res := firstMatch(eng.Match(core.DataPoint{Tag: "temp", Value: 25.0}))
	if res == nil {
		t.Fatal("expected match from rule-b")
	}
	if res.Rule.Name() != "rule-b" {
		t.Errorf("expected rule-b, got %s", res.Rule.Name())
	}

	// Verify disabled state in stats
	stats := eng.RuleStats()
	if !stats[0].Disabled {
		t.Error("expected rule-a to be disabled")
	}
	if stats[1].Disabled {
		t.Error("expected rule-b to be enabled")
	}

	// Re-enable rule-a
	if err := eng.SetRuleDisabled(0, false); err != nil {
		t.Fatalf("SetRuleDisabled failed: %v", err)
	}

	res2 := firstMatch(eng.Match(core.DataPoint{Tag: "temp", Value: 25.0}))
	if res2 == nil || res2.Rule.Name() != "rule-a" {
		t.Errorf("expected rule-a to match after re-enable, got %v", res2)
	}

	// Out of range index
	if err := eng.SetRuleDisabled(99, true); err == nil {
		t.Error("expected error for out-of-range index")
	}
}

func TestRuleDisableByName(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "rule-a", Match: "tag == 'temp'", Action: "forward", Target: "mqtt1", Priority: 10},
		{Name: "rule-b", Match: "ALL", Action: "forward", Target: "mqtt2", Priority: 100},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// Disable by name — the stable identifier across reloads.
	if err := eng.SetRuleDisabledByName("rule-b", true); err != nil {
		t.Fatalf("SetRuleDisabledByName failed: %v", err)
	}

	stats := eng.RuleStats()
	if stats[1].Name != "rule-b" || !stats[1].Disabled {
		t.Errorf("expected rule-b disabled, got name=%s disabled=%v", stats[1].Name, stats[1].Disabled)
	}
	if stats[0].Disabled {
		t.Error("rule-a must remain enabled")
	}

	// Matching skips the disabled rule even though it has the higher priority.
	res := firstMatch(eng.Match(core.DataPoint{Tag: "temp", Value: 25.0}))
	if res == nil || res.Rule.Name() != "rule-a" {
		t.Errorf("expected rule-a to match, got %v", res)
	}

	// Re-enable by name.
	if err := eng.SetRuleDisabledByName("rule-b", false); err != nil {
		t.Fatalf("re-enable failed: %v", err)
	}
	if eng.RuleStats()[1].Disabled {
		t.Error("expected rule-b enabled after re-enable")
	}

	// Unknown name errors — unlike index, an unknown name can never
	// silently address a different rule.
	if err := eng.SetRuleDisabledByName("no-such-rule", true); err == nil {
		t.Error("expected error for unknown rule name")
	}
}

func TestCELExpressions(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "bool-eq", Match: "value == true", Action: "forward", Target: "t1", Priority: 1},
		{Name: "neq", Match: "quality != 'good'", Action: "alert", Target: "t2", Priority: 2},
		{Name: "num-eq", Match: "value == 42", Action: "forward", Target: "t5", Priority: 3},
		{Name: "paren", Match: "(value > 50 && value < 100) || tag == 'override'", Action: "forward", Target: "t6", Priority: 4},
		{Name: "or", Match: "tag == 'temp' || tag == 'humidity'", Action: "forward", Target: "t3", Priority: 5},
		{Name: "not", Match: "!(tag == 'temp')", Action: "drop", Target: "t4", Priority: 6},
		{Name: "catchall", Match: "ALL", Action: "forward", Target: "t7", Priority: 999},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// value == true
	res := firstMatch(eng.Match(core.DataPoint{Tag: "x", Value: true}))
	if res == nil || res.Rule.Name() != "bool-eq" {
		t.Errorf("bool-eq failed: %v", res)
	}

	// quality != 'good'
	res = firstMatch(eng.Match(core.DataPoint{Tag: "x", Value: 1, Quality: core.QualityBad}))
	if res == nil || res.Rule.Name() != "neq" {
		t.Errorf("neq failed: %v", res)
	}

	// tag == 'temp' || tag == 'humidity'
	res = firstMatch(eng.Match(core.DataPoint{Tag: "humidity", Value: 1, Quality: core.QualityGood}))
	if res == nil || res.Rule.Name() != "or" {
		t.Errorf("or failed: %v", res)
	}

	// !(tag == 'temp')
	res = firstMatch(eng.Match(core.DataPoint{Tag: "pressure", Value: 1, Quality: core.QualityGood}))
	if res == nil || res.Rule.Name() != "not" {
		t.Errorf("not failed: %v", res)
	}

	// value == 42
	res = firstMatch(eng.Match(core.DataPoint{Tag: "x", Value: int32(42), Quality: core.QualityGood}))
	if res == nil || res.Rule.Name() != "num-eq" {
		t.Errorf("num-eq failed: %v", res)
	}

	// (value > 50 && value < 100) || tag == 'override'
	res = firstMatch(eng.Match(core.DataPoint{Tag: "x", Value: 75.0, Quality: core.QualityGood}))
	if res == nil || res.Rule.Name() != "paren" {
		t.Errorf("paren failed: %v", res)
	}
}

func TestDriverIndexMatch(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "plc-rule", Match: "driver == 'plc' && tag == 'temp'", Action: "forward", Target: "mqtt1", Priority: 10},
		{Name: "modbus-rule", Match: "driver == 'modbus' && tag == 'humidity'", Action: "forward", Target: "mqtt2", Priority: 10},
		{Name: "catchall", Match: "ALL", Action: "forward", Target: "mqtt3", Priority: 100},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// Data point from plc driver → should match plc-rule (priority 10)
	res := firstMatch(eng.Match(core.DataPoint{Driver: "plc", Tag: "temp", Value: 25.0}))
	if res == nil || res.Rule.Name() != "plc-rule" {
		t.Errorf("expected plc-rule, got %v", res)
	}

	// Data point from modbus driver → should match modbus-rule (priority 10)
	res = firstMatch(eng.Match(core.DataPoint{Driver: "modbus", Tag: "humidity", Value: 30.0}))
	if res == nil || res.Rule.Name() != "modbus-rule" {
		t.Errorf("expected modbus-rule, got %v", res)
	}

	// Data point from unknown driver → should match catchall (priority 100)
	res = firstMatch(eng.Match(core.DataPoint{Driver: "opcua", Tag: "x", Value: 1.0}))
	if res == nil || res.Rule.Name() != "catchall" {
		t.Errorf("expected catchall, got %v", res)
	}

	// Verify miss statistics: plc-rule should have misses from the modbus
	// and opcua data points (both have priority < 100, so they are
	// evaluated before catchall matches). modbus-rule should have a miss
	// from the opcua data point.
	stats := eng.RuleStats()
	statMap := make(map[string]core.RuleStat, len(stats))
	for _, s := range stats {
		statMap[s.Name] = s
	}

	// plc-rule: hit on plc/temp. Misses from the modbus and opcua data
	// points. The modbus point's evaluation order is [plc-rule(seq0)@10,
	// modbus-rule(seq1)@10, ...]: with the config-order tie-break (B7-1),
	// plc-rule is reached (and misses) before modbus-rule terminates, so
	// it records a miss there too — plus one from the opcua point where
	// it is evaluated before catchall matches. Total: 2.
	// modbus-rule misses only on the opcua point: on the plc point its
	// config position (seq1) is at/after the terminal (seq0), so a linear
	// scan would not reach it.
	if statMap["plc-rule"].MissCount != 2 {
		t.Errorf("plc-rule miss count: expected 2 (reached before terminal on modbus point + opcua point), got %d", statMap["plc-rule"].MissCount)
	}
	if statMap["modbus-rule"].MissCount != 1 {
		t.Errorf("modbus-rule miss count: expected 1 (from opcua point), got %d", statMap["modbus-rule"].MissCount)
	}
	if statMap["plc-rule"].HitCount != 1 {
		t.Errorf("plc-rule hit count: expected 1, got %d", statMap["plc-rule"].HitCount)
	}
	if statMap["modbus-rule"].HitCount != 1 {
		t.Errorf("modbus-rule hit count: expected 1, got %d", statMap["modbus-rule"].HitCount)
	}
}

func TestDriverIndexNoMatchStats(t *testing.T) {
	eng := NewEngine()

	configs := []core.RuleConfig{
		{Name: "plc-rule", Match: "driver == 'plc' && tag == 'temp'", Action: "forward", Target: "mqtt1", Priority: 10},
		{Name: "modbus-rule", Match: "driver == 'modbus' && tag == 'humidity'", Action: "forward", Target: "mqtt2", Priority: 20},
	}

	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// No match for any driver — all rules should record misses.
	res := firstMatch(eng.Match(core.DataPoint{Driver: "plc", Tag: "pressure", Value: 1.0}))
	if res != nil {
		t.Errorf("expected no match, got %v", res)
	}

	stats := eng.RuleStats()
	statMap := make(map[string]core.RuleStat, len(stats))
	for _, s := range stats {
		statMap[s.Name] = s
	}

	// plc-rule: evaluated and missed (driver matches, tag doesn't)
	if statMap["plc-rule"].MissCount != 1 {
		t.Errorf("plc-rule miss count: expected 1, got %d", statMap["plc-rule"].MissCount)
	}
	// modbus-rule: not evaluated (different driver), but no match found
	// so all rules would have been evaluated → RecordMiss called
	if statMap["modbus-rule"].MissCount != 1 {
		t.Errorf("modbus-rule miss count: expected 1, got %d", statMap["modbus-rule"].MissCount)
	}
}
