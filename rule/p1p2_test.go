package rule

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// ─── P1-1: Regex / Contains / Suffix / Prefix ───────────────────────

func TestRegexMatch(t *testing.T) {
	p := core.DataPoint{Tag: "reactor_temp_01", Driver: "modbus1"}

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

	// =~ regex match
	check("tag =~ '^reactor_.*'", true)
	check("tag =~ '^sensor_.*'", false)
	check("tag =~ 'temp'", true)
	check("tag =~ '[0-9]+$'", true)

	// !~ regex non-match
	check("tag !~ '^sensor_.*'", true)
	check("tag !~ '^reactor_.*'", false)
}

func TestContainsSuffixPrefix(t *testing.T) {
	p := core.DataPoint{Tag: "reactor_temp_01", Driver: "modbus-tcp"}

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

	// contains
	check("tag contains 'temp'", true)
	check("tag contains 'pressure'", false)
	check("driver contains 'modbus'", true)

	// suffix
	check("tag suffix '_01'", true)
	check("tag suffix '_02'", false)

	// prefix
	check("tag prefix 'reactor'", true)
	check("tag prefix 'sensor'", false)
	check("driver prefix 'modbus'", true)
}

func TestRegexInEngine(t *testing.T) {
	eng := NewEngine()
	configs := []core.RuleConfig{
		{Name: "reactor-rule", Match: "tag =~ '^reactor_.*' && value > 80", Action: "alert", Target: "t1", Priority: 1},
		{Name: "catchall", Match: "ALL", Action: "forward", Target: "t2", Priority: 999},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// Should match reactor-rule
	res := eng.Match(core.DataPoint{Tag: "reactor_temp", Value: 90.0})
	if res == nil || res.Rule.Name() != "reactor-rule" {
		t.Errorf("expected reactor-rule, got %v", res)
	}

	// Should NOT match reactor-rule (value too low)
	res = eng.Match(core.DataPoint{Tag: "reactor_temp", Value: 50.0})
	if res == nil || res.Rule.Name() != "catchall" {
		t.Errorf("expected catchall, got %v", res)
	}

	// Should NOT match reactor-rule (tag doesn't match regex)
	res = eng.Match(core.DataPoint{Tag: "sensor_temp", Value: 90.0})
	if res == nil || res.Rule.Name() != "catchall" {
		t.Errorf("expected catchall, got %v", res)
	}
}

// ─── P1-2: Range ─────────────────────────────────────────────────────

func TestRangeMatch(t *testing.T) {
	p := core.DataPoint{Tag: "temp", Value: 75.0}

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

	// value in 50..100
	check("value in 50..100", true)
	check("value in 80..100", false)
	check("value in 50..70", false)

	// boundary
	p2 := core.DataPoint{Tag: "temp", Value: 50.0}
	node, _ := compileExpr("value in 50..100")
	if !node.eval(p2) {
		t.Error("expected 50 to be in 50..100 (inclusive)")
	}
	p3 := core.DataPoint{Tag: "temp", Value: 100.0}
	if !node.eval(p3) {
		t.Error("expected 100 to be in 50..100 (inclusive)")
	}
}

func TestRangeInEngine(t *testing.T) {
	eng := NewEngine()
	configs := []core.RuleConfig{
		{Name: "normal-range", Match: "value in 50..100", Action: "forward", Target: "t1", Priority: 1},
		{Name: "catchall", Match: "ALL", Action: "forward", Target: "t2", Priority: 999},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	res := eng.Match(core.DataPoint{Tag: "temp", Value: 75.0})
	if res == nil || res.Rule.Name() != "normal-range" {
		t.Errorf("expected normal-range, got %v", res)
	}

	res = eng.Match(core.DataPoint{Tag: "temp", Value: 200.0})
	if res == nil || res.Rule.Name() != "catchall" {
		t.Errorf("expected catchall, got %v", res)
	}
}

// ─── P2-1: Rule Provider ─────────────────────────────────────────────

func TestFileProvider(t *testing.T) {
	// Create a temp rule file
	dir := t.TempDir()
	ruleFile := filepath.Join(dir, "safety-rules.yaml")
	ruleContent := `
rules:
  - name: bad-quality
    match: "quality != 'good'"
  - name: high-value
    match: "value > 100"
  - name: emergency-tag
    match: "tag suffix 'emergency'"
`
	if err := os.WriteFile(ruleFile, []byte(ruleContent), 0o644); err != nil {
		t.Fatalf("failed to write rule file: %v", err)
	}

	provider, err := NewFileProvider("safety", ruleFile, "")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	if provider.Count() != 3 {
		t.Errorf("expected 3 rules, got %d", provider.Count())
	}

	// Should match: quality != 'good'
	if !provider.Match(core.DataPoint{Tag: "t", Value: 1, Quality: core.QualityBad}) {
		t.Error("expected match for bad quality")
	}

	// Should match: value > 100
	if !provider.Match(core.DataPoint{Tag: "t", Value: 150, Quality: core.QualityGood}) {
		t.Error("expected match for high value")
	}

	// Should match: tag suffix 'emergency'
	if !provider.Match(core.DataPoint{Tag: "pump_emergency", Value: 1, Quality: core.QualityGood}) {
		t.Error("expected match for emergency tag")
	}

	// Should NOT match
	if provider.Match(core.DataPoint{Tag: "normal", Value: 50, Quality: core.QualityGood}) {
		t.Error("expected no match for normal point")
	}
}

func TestRuleSetInEngine(t *testing.T) {
	// Create a temp rule file
	dir := t.TempDir()
	ruleFile := filepath.Join(dir, "alert-rules.yaml")
	ruleContent := `
rules:
  - name: high-temp
    match: "tag == 'temperature' && value > 90"
  - name: bad-quality
    match: "quality != 'good'"
`
	if err := os.WriteFile(ruleFile, []byte(ruleContent), 0o644); err != nil {
		t.Fatalf("failed to write rule file: %v", err)
	}

	provider, err := NewFileProvider("alert-rules", ruleFile, "")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	eng := NewEngine()
	eng.AddProvider(provider)

	configs := []core.RuleConfig{
		{Name: "use-alert-rules", Match: "RULE-SET:alert-rules", Action: "alert", Target: "cloud-mqtt", Priority: 1},
		{Name: "catchall", Match: "ALL", Action: "forward", Target: "default", Priority: 999},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// Should match via rule-set: high temp
	res := eng.Match(core.DataPoint{Tag: "temperature", Value: 95.0, Quality: core.QualityGood})
	if res == nil || res.Rule.Name() != "use-alert-rules" {
		t.Errorf("expected use-alert-rules, got %v", res)
	}

	// Should match via rule-set: bad quality
	res = eng.Match(core.DataPoint{Tag: "pressure", Value: 50.0, Quality: core.QualityBad})
	if res == nil || res.Rule.Name() != "use-alert-rules" {
		t.Errorf("expected use-alert-rules, got %v", res)
	}

	// Should NOT match rule-set, fall through to catchall
	res = eng.Match(core.DataPoint{Tag: "pressure", Value: 50.0, Quality: core.QualityGood})
	if res == nil || res.Rule.Name() != "catchall" {
		t.Errorf("expected catchall, got %v", res)
	}
}

func TestFileProviderHotReload(t *testing.T) {
	dir := t.TempDir()
	ruleFile := filepath.Join(dir, "dynamic-rules.yaml")

	// Initial content
	os.WriteFile(ruleFile, []byte("rules:\n  - name: r1\n    match: \"value > 50\"\n"), 0o644)

	provider, err := NewFileProvider("dynamic", ruleFile, "50ms")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	// Initially matches value > 50
	if !provider.Match(core.DataPoint{Value: 60.0}) {
		t.Error("expected match for value=60")
	}
	if provider.Match(core.DataPoint{Value: 30.0}) {
		t.Error("expected no match for value=30")
	}

	// Update file: change threshold to value > 20
	os.WriteFile(ruleFile, []byte("rules:\n  - name: r1\n    match: \"value > 20\"\n"), 0o644)

	// Poll until reload takes effect (replaces fixed sleep).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if provider.Match(core.DataPoint{Value: 30.0}) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Now value=30 should match
	if !provider.Match(core.DataPoint{Value: 30.0}) {
		t.Error("expected match for value=30 after reload")
	}
}

// ─── P2-2: SUB-RULE ─────────────────────────────────────────────────

func TestSubRule(t *testing.T) {
	eng := NewEngine()

	subGroups := map[string][]core.RuleConfig{
		"safety-check": {
			{Name: "bad-quality", Match: "quality != 'good'", Action: "alert", Target: "alerts", Priority: 1},
			{Name: "emergency-tag", Match: "tag suffix 'emergency'", Action: "alert", Target: "alerts", Priority: 2},
		},
	}
	if err := eng.SetSubRules(subGroups); err != nil {
		t.Fatalf("SetSubRules failed: %v", err)
	}

	configs := []core.RuleConfig{
		{Name: "use-safety", Match: "SUB-RULE:safety-check", Action: "alert", Target: "cloud-mqtt", Priority: 1},
		{Name: "catchall", Match: "ALL", Action: "forward", Target: "default", Priority: 999},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// Should match via sub-rule: bad quality
	res := eng.Match(core.DataPoint{Tag: "temp", Value: 50.0, Quality: core.QualityBad})
	if res == nil || res.Rule.Name() != "use-safety" {
		t.Errorf("expected use-safety, got %v", res)
	}

	// Should match via sub-rule: emergency tag
	res = eng.Match(core.DataPoint{Tag: "pump_emergency", Value: 50.0, Quality: core.QualityGood})
	if res == nil || res.Rule.Name() != "use-safety" {
		t.Errorf("expected use-safety, got %v", res)
	}

	// Should NOT match sub-rule, fall through to catchall
	res = eng.Match(core.DataPoint{Tag: "normal", Value: 50.0, Quality: core.QualityGood})
	if res == nil || res.Rule.Name() != "catchall" {
		t.Errorf("expected catchall, got %v", res)
	}
}

func TestSubRuleCircularDetection(t *testing.T) {
	eng := NewEngine()

	// a -> b -> a (circular)
	subGroups := map[string][]core.RuleConfig{
		"a": {{Name: "ref-b", Match: "SUB-RULE:b", Action: "forward", Target: "t"}},
		"b": {{Name: "ref-a", Match: "SUB-RULE:a", Action: "forward", Target: "t"}},
	}
	err := eng.SetSubRules(subGroups)
	if err == nil {
		t.Fatal("expected circular reference error, got nil")
	}
}

func TestSubRuleMissingRef(t *testing.T) {
	eng := NewEngine()

	subGroups := map[string][]core.RuleConfig{
		"a": {{Name: "ref-missing", Match: "SUB-RULE:nonexistent", Action: "forward", Target: "t"}},
	}
	err := eng.SetSubRules(subGroups)
	if err == nil {
		t.Fatal("expected missing reference error, got nil")
	}
}

// ─── P0-2: Wrapper stats verification ────────────────────────────────

func TestWrapperStats(t *testing.T) {
	eng := NewEngine()
	configs := []core.RuleConfig{
		{Name: "regex-rule", Match: "tag =~ '^temp_.*'", Action: "forward", Target: "t1", Priority: 1},
		{Name: "catchall", Match: "ALL", Action: "forward", Target: "t2", Priority: 999},
	}
	if err := eng.SetRules(configs); err != nil {
		t.Fatalf("SetRules failed: %v", err)
	}

	// Match: regex-rule hits, catchall hits
	eng.Match(core.DataPoint{Tag: "temp_01", Value: 25.0})
	// Match: regex-rule misses, catchall hits
	eng.Match(core.DataPoint{Tag: "pressure", Value: 30.0})

	stats := eng.RuleStats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(stats))
	}

	// regex-rule
	if stats[0].HitCount != 1 {
		t.Errorf("regex-rule hits: expected 1, got %d", stats[0].HitCount)
	}
	if stats[0].MissCount != 1 {
		t.Errorf("regex-rule misses: expected 1, got %d", stats[0].MissCount)
	}
	if stats[0].Type != "simple" {
		t.Errorf("expected type 'simple', got %s", stats[0].Type)
	}

	// catchall: only evaluated (and hit) when regex-rule misses
	if stats[1].HitCount != 1 {
		t.Errorf("catchall hits: expected 1, got %d", stats[1].HitCount)
	}
}

func TestRuleSetStats(t *testing.T) {
	dir := t.TempDir()
	ruleFile := filepath.Join(dir, "rs.yaml")
	os.WriteFile(ruleFile, []byte("rules:\n  - name: r1\n    match: \"value > 50\"\n"), 0o644)

	provider, _ := NewFileProvider("rs", ruleFile, "")
	defer provider.Close()

	eng := NewEngine()
	eng.AddProvider(provider)

	eng.SetRules([]core.RuleConfig{
		{Name: "use-rs", Match: "RULE-SET:rs", Action: "alert", Target: "t1", Priority: 1},
		{Name: "catchall", Match: "ALL", Action: "forward", Target: "t2", Priority: 999},
	})

	eng.Match(core.DataPoint{Value: 60.0})
	eng.Match(core.DataPoint{Value: 30.0})

	stats := eng.RuleStats()
	if stats[0].Type != "rule-set" {
		t.Errorf("expected type 'rule-set', got %s", stats[0].Type)
	}
	if stats[0].HitCount != 1 {
		t.Errorf("rule-set hits: expected 1, got %d", stats[0].HitCount)
	}
	if stats[0].MissCount != 1 {
		t.Errorf("rule-set misses: expected 1, got %d", stats[0].MissCount)
	}
}
