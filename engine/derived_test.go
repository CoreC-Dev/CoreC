package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestExtractVars(t *testing.T) {
	vars := extractVars("output_power / input_power * 100")
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars, got %v", vars)
	}
	want := map[string]bool{"output_power": true, "input_power": true}
	for _, v := range vars {
		if !want[v] {
			t.Fatalf("unexpected var %q", v)
		}
	}
}

func TestExtractVarsFiltersBuiltins(t *testing.T) {
	vars := extractVars("abs(temp1 - temp2)")
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars (temp1, temp2), got %v", vars)
	}
}

func TestToFloat64(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{float64(3.14), 3.14, true},
		{float32(2.5), 2.5, true},
		{int(42), 42, true},
		{uint16(100), 100, true},
		{int32(-7), -7, true},
		{true, 1, true},
		{false, 0, true},
		{"hello", 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := toFloat64(c.in)
		if ok != c.ok || (c.ok && got != c.want) {
			t.Errorf("toFloat64(%v) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestBuildEnvMissing(t *testing.T) {
	snapshot := map[string]core.DataPoint{
		"a": {Tag: "a", Value: float64(10), Timestamp: time.Now()},
	}
	env, missing, stale := buildEnv([]string{"a", "b"}, nil, snapshot, NewLatestCache(), 0)
	if env["a"].(float64) != 10 {
		t.Fatalf("env[a] = %v, want 10", env["a"])
	}
	if len(missing) != 1 || missing[0] != "b" {
		t.Fatalf("missing = %v, want [b]", missing)
	}
	if stale {
		t.Fatal("should not be stale")
	}
}

func TestBuildEnvStale(t *testing.T) {
	old := time.Now().Add(-2 * time.Minute)
	snapshot := map[string]core.DataPoint{
		"a": {Tag: "a", Value: float64(10), Timestamp: old},
	}
	_, _, stale := buildEnv([]string{"a"}, nil, snapshot, NewLatestCache(), time.Minute)
	if !stale {
		t.Fatal("should be stale")
	}
}

func TestCompileDerivedTagValid(t *testing.T) {
	ct, err := compileDerivedTag(core.DerivedTagConfig{
		Name:     "efficiency",
		Expr:     "output / input * 100",
		Type:     core.TypeFloat32,
		Interval: "5s",
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if ct.interval != 5*time.Second {
		t.Fatalf("interval = %v", ct.interval)
	}
	if len(ct.vars) != 2 {
		t.Fatalf("vars = %v", ct.vars)
	}
}

func TestCompileDerivedTagBadInterval(t *testing.T) {
	_, err := compileDerivedTag(core.DerivedTagConfig{
		Name: "x", Expr: "a + b", Type: core.TypeFloat32, Interval: "100ms",
	})
	if err == nil {
		t.Fatal("expected error for interval < 1s")
	}
}

func TestCompileDerivedTagBadExpr(t *testing.T) {
	_, err := compileDerivedTag(core.DerivedTagConfig{
		Name: "x", Expr: "a +", Type: core.TypeFloat32, Interval: "5s",
	})
	if err == nil {
		t.Fatal("expected error for bad expr")
	}
}

func TestCompileDerivedTagOnMissing(t *testing.T) {
	for _, policy := range []string{"skip", "bad-quality", "zero", ""} {
		ct, err := compileDerivedTag(core.DerivedTagConfig{
			Name: "x", Expr: "a", Type: core.TypeFloat32, Interval: "5s", OnMissing: policy,
		})
		if err != nil {
			t.Fatalf("on-missing %q: %v", policy, err)
		}
		_ = ct
	}
	_, err := compileDerivedTag(core.DerivedTagConfig{
		Name: "x", Expr: "a", Type: core.TypeFloat32, Interval: "5s", OnMissing: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for bogus on-missing")
	}
}

func TestValidateDerivedTags(t *testing.T) {
	err := validateDerivedTags([]core.DerivedTagConfig{
		{Name: "a", Expr: "x + y", Interval: "5s"},
		{Name: "a", Expr: "x - y", Interval: "5s"},
	})
	if err == nil {
		t.Fatal("expected duplicate name error")
	}
	err = validateDerivedTags([]core.DerivedTagConfig{
		{Name: "", Expr: "x", Interval: "5s"},
	})
	if err == nil {
		t.Fatal("expected empty name error")
	}
	err = validateDerivedTags([]core.DerivedTagConfig{
		{Name: "a", Expr: "", Interval: "5s"},
	})
	if err == nil {
		t.Fatal("expected empty expr error")
	}
}

func TestNewDerivedTagEvaluatorEmpty(t *testing.T) {
	ev, err := newDerivedTagEvaluator(nil, NewLatestCache(), NewDataBus(64))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev == nil {
		t.Fatal("expected non-nil evaluator for empty config")
	}
	if len(ev.tags) != 0 {
		t.Fatalf("expected 0 tags, got %d", len(ev.tags))
	}
}

func TestEvaluateEndToEnd(t *testing.T) {
	cache := NewLatestCache()
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "output_power", Value: float64(80), Type: core.TypeFloat32, Timestamp: time.Now()})
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "input_power", Value: float64(100), Type: core.TypeFloat32, Timestamp: time.Now()})

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "efficiency", Expr: "output_power / input_power * 100", Type: core.TypeFloat32, Interval: "5s"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}

	ev.evaluate(ev.tags[0])

	select {
	case point := <-bus.Channel():
		if point.Tag != "efficiency" {
			t.Fatalf("tag = %s", point.Tag)
		}
		if point.Value.(float64) != 80 {
			t.Fatalf("value = %v, want 80", point.Value)
		}
		if point.Driver != "derived" {
			t.Fatalf("driver = %s", point.Driver)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for derived point")
	}
}

func TestEvaluateMissingSkip(t *testing.T) {
	cache := NewLatestCache()
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "a", Value: float64(10), Timestamp: time.Now()})
	// "b" is missing

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "sum", Expr: "a + b", Type: core.TypeFloat32, Interval: "5s", OnMissing: "skip"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}

	ev.evaluate(ev.tags[0])

	select {
	case <-bus.Channel():
		t.Fatal("should not emit when on-missing=skip")
	case <-time.After(50 * time.Millisecond):
		// correct: no point emitted
	}
}

func TestEvaluateMissingZero(t *testing.T) {
	cache := NewLatestCache()
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "a", Value: float64(10), Timestamp: time.Now()})

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "sum", Expr: "a + b", Type: core.TypeFloat32, Interval: "5s", OnMissing: "zero"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}

	ev.evaluate(ev.tags[0])

	select {
	case point := <-bus.Channel():
		if point.Value.(float64) != 10 {
			t.Fatalf("value = %v, want 10 (a=10, b=0)", point.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestEvaluateMissingBadQuality(t *testing.T) {
	cache := NewLatestCache()
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "a", Value: float64(10), Timestamp: time.Now()})

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "sum", Expr: "a + b", Type: core.TypeFloat32, Interval: "5s", OnMissing: "bad-quality"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}

	ev.evaluate(ev.tags[0])

	select {
	case point := <-bus.Channel():
		if point.Quality != core.QualityBad {
			t.Fatalf("quality = %v, want QualityBad", point.Quality)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestEvaluateDivByZero(t *testing.T) {
	cache := NewLatestCache()
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "a", Value: float64(10), Timestamp: time.Now()})
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "b", Value: float64(0), Timestamp: time.Now()})

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "ratio", Expr: "a / b", Type: core.TypeFloat32, Interval: "5s", OnMissing: "bad-quality"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}

	ev.evaluate(ev.tags[0])

	select {
	case point := <-bus.Channel():
		if point.Quality != core.QualityBad {
			t.Fatalf("quality = %v, want QualityBad for div-by-zero", point.Quality)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestEvaluateStaleGuard(t *testing.T) {
	cache := NewLatestCache()
	old := time.Now().Add(-2 * time.Minute)
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "a", Value: float64(10), Timestamp: old})
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "b", Value: float64(20), Timestamp: time.Now()})

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "sum", Expr: "a + b", Type: core.TypeFloat32, Interval: "5s", StaleGuard: "1m"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}

	ev.evaluate(ev.tags[0])

	select {
	case point := <-bus.Channel():
		if !point.IsStale {
			t.Fatal("expected IsStale=true")
		}
		if point.Quality != core.QualityBad {
			t.Fatalf("quality = %v, want QualityBad for stale", point.Quality)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestEvaluateUnit(t *testing.T) {
	cache := NewLatestCache()
	cache.Update(core.DataPoint{Driver: "modbus", Tag: "a", Value: float64(42), Timestamp: time.Now()})

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "kpi", Expr: "a", Type: core.TypeFloat32, Interval: "5s", Unit: "%"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}

	ev.evaluate(ev.tags[0])

	select {
	case point := <-bus.Channel():
		if point.Metadata["unit"] != "%" {
			t.Fatalf("unit = %v", point.Metadata["unit"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

// TestDerivedQualifiedRefs pins the driver.tag qualified-reference fix
// (audit finding PIPE-2): the flattened cache view collapses same-named
// tags across drivers, so `temp` alone read whichever driver won the
// map race. Expressions can now name the exact pair: `plc1.temp`.
func TestDerivedQualifiedRefs(t *testing.T) {
	cache := NewLatestCache()
	// Two drivers, same tag name, different values — the collision case.
	cache.Update(core.DataPoint{Driver: "plc1", Tag: "temp", Value: float64(90), Timestamp: time.Now()})
	cache.Update(core.DataPoint{Driver: "plc2", Tag: "temp", Value: float64(40), Timestamp: time.Now()})
	cache.Update(core.DataPoint{Driver: "plc2", Tag: "hum", Value: float64(55), Timestamp: time.Now()})

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "spread", Expr: "plc1.temp - plc2.temp", Type: core.TypeFloat64, Interval: "5s"},
		{Name: "legacy", Expr: "hum", Type: core.TypeFloat64, Interval: "5s"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}

	byTag := make(map[string]core.DataPoint)
	ev.evaluate(ev.tags[0]) // spread
	ev.evaluate(ev.tags[1]) // legacy
	for i := 0; i < 2; i++ {
		select {
		case dp := <-bus.Channel():
			byTag[dp.Tag] = dp
		default:
		}
	}

	if got := byTag["spread"]; got.Value != float64(50) {
		t.Errorf("plc1.temp - plc2.temp = %v, want 50 (qualified refs must read distinct drivers)", got.Value)
	}
	if got := byTag["legacy"]; got.Value != float64(55) {
		t.Errorf("unqualified `hum` = %v, want 55 (legacy flat refs keep working)", got.Value)
	}
}

// TestDerivedQualifiedRefsMissingPolicy verifies missing qualified refs
// flow through the on-missing policies exactly like flat refs.
func TestDerivedQualifiedRefsMissingPolicy(t *testing.T) {
	cache := NewLatestCache()
	cache.Update(core.DataPoint{Driver: "plc1", Tag: "temp", Value: float64(90), Timestamp: time.Now()})
	// plc2 has NO temp — the qualified ref is missing.

	bus := NewDataBus(64)
	ev, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "sum", Expr: "plc1.temp + plc2.temp", Type: core.TypeFloat64, Interval: "5s", OnMissing: "bad-quality"},
	}, cache, bus)
	if err != nil {
		t.Fatalf("newDerivedTagEvaluator: %v", err)
	}
	ev.evaluate(ev.tags[0])

	select {
	case dp := <-bus.Channel():
		if dp.Quality != core.QualityBad {
			t.Errorf("quality = %v, want QualityBad for a missing qualified ref", dp.Quality)
		}
		if dp.Value != float64(90) {
			t.Errorf("value = %v, want 90 (missing side zero-injects: 90 + 0)", dp.Value)
		}
	default:
		t.Fatal("no point pushed for bad-quality missing policy")
	}
}

// TestExtractQualifiedRefs verifies the extraction: qualified pairs are
// captured, deduplicated, and excluded from the flat var list.
func TestExtractQualifiedRefs(t *testing.T) {
	refs := extractQualifiedRefs("plc1.temp + plc2.temp * plc1.temp + temp + len([1,2])")
	if len(refs) != 2 {
		t.Fatalf("qualified refs = %+v, want 2 unique pairs", refs)
	}
	if refs[0] != (qualifiedRef{driver: "plc1", tag: "temp"}) || refs[1] != (qualifiedRef{driver: "plc2", tag: "temp"}) {
		t.Errorf("refs = %+v, want plc1.temp then plc2.temp in order", refs)
	}
	vars := extractVars("plc1.temp + plc2.temp * plc1.temp + temp + len([1,2])")
	// Only the standalone `temp` survives the qualified blanking; `len`
	// is a builtin.
	if len(vars) != 1 || vars[0] != "temp" {
		t.Errorf("flat vars = %v, want just [temp]", vars)
	}
}

// TestDerivedCycleDetection pins roadmap #38 (audit PIPE-4): two derived
// tags referencing each other previously evaluated silently forever —
// every tick computed garbage from ghost values with no signal
// anywhere. Compilation now fails fast with the cycle path spelled out.
func TestDerivedCycleDetection(t *testing.T) {
	// Direct cycle: a -> b -> a.
	_, err := newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "a", Expr: "derived.b * 2", Interval: "1s"},
		{Name: "b", Expr: "derived.a + 1", Interval: "1s"},
	}, NewLatestCache(), NewDataBus(64))
	if err == nil {
		t.Fatal("expected cycle error for a -> b -> a")
	}
	if !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "a -> b -> a") {
		t.Errorf("error should spell the cycle path, got: %v", err)
	}

	// Self-reference.
	_, err = newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "self", Expr: "derived.self * 2", Interval: "1s"},
	}, NewLatestCache(), NewDataBus(64))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("self-reference should be a cycle error, got: %v", err)
	}

	// Unqualified-name cycle (the flat view also resolves derived names).
	_, err = newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "x", Expr: "y + 1", Interval: "1s"},
		{Name: "y", Expr: "x + 1", Interval: "1s"},
	}, NewLatestCache(), NewDataBus(64))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("unqualified cycle should error, got: %v", err)
	}

	// Acyclic chain is fine: c reads b reads a.
	_, err = newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "a", Expr: "raw * 2", Interval: "1s"},
		{Name: "b", Expr: "derived.a + 1", Interval: "1s"},
		{Name: "c", Expr: "derived.b * 3", Interval: "1s"},
	}, NewLatestCache(), NewDataBus(64))
	if err != nil {
		t.Errorf("acyclic derived chain rejected: %v", err)
	}

	// A derived tag shadowing a real driver tag name is NOT a cycle:
	// `driver.tag` qualified refs resolve per-driver, and the qualified
	// edge only exists when the target is itself a derived tag.
	_, err = newDerivedTagEvaluator([]core.DerivedTagConfig{
		{Name: "pressure", Expr: "plc1.pressure * 2", Interval: "1s"},
	}, NewLatestCache(), NewDataBus(64))
	if err != nil {
		t.Errorf("driver-tag reference misidentified as a cycle: %v", err)
	}
}
