package engine

import (
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
	env, missing, stale := buildEnv([]string{"a", "b"}, snapshot, 0)
	if env["a"] != 10 {
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
	_, _, stale := buildEnv([]string{"a"}, snapshot, time.Minute)
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
