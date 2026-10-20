package engine

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// collectFor gathers every point that arrives on ch during the window,
// failing fast when the wanted tag/value pair shows up early.
func waitForDerived(t *testing.T, ch <-chan core.DataPoint, tag string, want float64, window time.Duration) core.DataPoint {
	t.Helper()
	deadline := time.After(window)
	for {
		select {
		case dp := <-ch:
			if dp.Tag == tag && dp.Value == want {
				return dp
			}
			// Unrelated derived point (or a duplicate of an older
			// tick) — keep waiting.
		case <-deadline:
			t.Fatalf("timed out waiting for derived %s = %v within %v", tag, want, window)
			return core.DataPoint{}
		}
	}
}

// assertNoDerived asserts that no point for the given tag arrives
// within the window (used to prove the pre-swap evaluator stopped).
func assertNoDerived(t *testing.T, ch <-chan core.DataPoint, tag string, window time.Duration) {
	t.Helper()
	deadline := time.After(window)
	for {
		select {
		case dp := <-ch:
			if dp.Tag == tag {
				t.Fatalf("derived %s still ticking after swap: %+v", tag, dp)
			}
		case <-deadline:
			return
		}
	}
}

// drainDerived empties anything buffered on ch so the subsequent
// absence assertions start from a clean slate.
func drainDerived(ch <-chan core.DataPoint) {
	for {
		select {
		case <-ch:
			continue
		default:
			return
		}
	}
}

// TestApplyDerivedTagsHotSwap pins the derived-tags hot-reload semantics
// (audit finding CFG-1): PUT /configs with changed derived-tags
// previously returned success while nothing happened.
//
//   - the new set compiles and starts ticking within one interval
//   - the old set stops exactly at the swap (no zombie goroutines)
//   - a broken expression keeps the previous set running (fail-safe)
//   - an empty set removes all derived tags
//   - applying to a never-started engine errors instead of corrupting
func TestApplyDerivedTagsHotSwap(t *testing.T) {
	e := New().(*CoreCEngine)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &core.Config{
		DerivedTags: []core.DerivedTagConfig{
			{Name: "kpi1", Expr: "x * 2", Interval: "1s"},
		},
	}
	if err := e.Start(ctx, cfg); err != nil {
		t.Fatalf("engine start: %v", err)
	}
	defer e.Stop()

	// Seed the source tag the expressions read from.
	e.processing.cache.Update(core.DataPoint{
		Driver: "plc", Tag: "x", Value: float64(5),
		Type: core.TypeFloat64, Quality: core.QualityGood, Timestamp: time.Now(),
	})

	ch, unsub := e.Subscribe(derivedDriverName)
	defer unsub()

	// The initial set computes kpi1 = 5 * 2 = 10.
	waitForDerived(t, ch, "kpi1", 10, 10*time.Second)

	// Hot-swap to kpi2 = 5 * 3 = 15.
	if err := e.ApplyDerivedTags([]core.DerivedTagConfig{
		{Name: "kpi2", Expr: "x * 3", Interval: "1s"},
	}); err != nil {
		t.Fatalf("ApplyDerivedTags: %v", err)
	}
	waitForDerived(t, ch, "kpi2", 15, 10*time.Second)

	// kpi1 must be gone: the old evaluator was stopped at the swap.
	drainDerived(ch)
	assertNoDerived(t, ch, "kpi1", 1500*time.Millisecond)

	// A broken expression must NOT tear down the running set: the new
	// set is compiled before the old one is stopped.
	before := e.lifecycle.derivedEvaluator
	if err := e.ApplyDerivedTags([]core.DerivedTagConfig{
		{Name: "broken", Expr: "this is (not valid", Interval: "1s"},
	}); err == nil {
		t.Fatal("expected compile error for broken expression")
	}
	if e.lifecycle.derivedEvaluator != before {
		t.Fatal("evaluator was replaced despite a failed apply — the old set must keep running")
	}
	// And the surviving set still ticks.
	waitForDerived(t, ch, "kpi2", 15, 10*time.Second)

	// An empty set removes all derived tags.
	if err := e.ApplyDerivedTags(nil); err != nil {
		t.Fatalf("ApplyDerivedTags(nil): %v", err)
	}
	drainDerived(ch)
	assertNoDerived(t, ch, "kpi2", 1500*time.Millisecond)

	// Applying to a never-started engine errors instead of installing
	// an evaluator bound to a nil context.
	e2 := New().(*CoreCEngine)
	if err := e2.ApplyDerivedTags([]core.DerivedTagConfig{
		{Name: "k", Expr: "1", Interval: "1s"},
	}); err == nil {
		t.Fatal("expected error applying derived tags to an unstarted engine")
	}
}
