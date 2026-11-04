package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/common/metrics"
	"github.com/CoreC-Dev/CoreC/core"
)

// triggerRecorder stands in for the scheduler's readFunc: it answers every tag
// request with a configured value and records which tags each round read.
type triggerRecorder struct {
	mu     sync.Mutex
	values map[string]any
	reads  [][]string
}

func (r *triggerRecorder) read(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads = append(r.reads, append([]string(nil), tags...))
	out := make([]core.TagValue, 0, len(tags))
	for _, tag := range tags {
		val, ok := r.values[tag]
		if !ok {
			val = float64(0)
		}
		out = append(out, core.TagValue{Tag: tag, Value: val, Quality: core.QualityGood, Timestamp: time.Now()})
	}
	return out, nil
}

func (r *triggerRecorder) set(tag string, v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[tag] = v
}

func (r *triggerRecorder) rounds() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.reads))
	copy(out, r.reads)
	return out
}

func hasTag(r []string, tag string) bool {
	for _, x := range r {
		if x == tag {
			return true
		}
	}
	return false
}

func newTriggerRunner() *taskRunner {
	return &taskRunner{
		task: core.ScheduleTask{
			ID:     "t",
			Driver: "plc",
			Tags:   []string{"alarm", "d1", "d2"},
		},
		groupTrigger: map[string]string{"detail": "alarm"},
		gated:        map[string][]string{"detail": {"d1", "d2"}},
		lastValues:   map[string]float64{},
		lastSent:     map[string]time.Time{},
	}
}

// TestTriggerGroupSkipsDetailsWhenConditionFalse is the core contract: while
// the condition is false the detail tags are never requested, the condition
// itself still is, and the skipped detail tags are reported in the plan so the
// engine can republish them as last-known-good/uncertain.
func TestTriggerGroupSkipsDetailsWhenConditionFalse(t *testing.T) {
	rec := &triggerRecorder{values: map[string]any{"alarm": false}}
	runner := newTriggerRunner()

	plan := runner.gateTriggerTags(context.Background(), runner.task, rec.read)
	if len(plan.conditions) != 1 || plan.conditions[0].Tag != "alarm" {
		t.Fatalf("condition read returned %+v, want the alarm tag", plan.conditions)
	}
	if len(plan.read) != 0 {
		t.Fatalf("detail tags must be skipped while the condition is false, got %v", plan.read)
	}
	if !hasTag(plan.suppressed, "d1") || !hasTag(plan.suppressed, "d2") {
		t.Fatalf("suppressed details must be reported, got %v", plan.suppressed)
	}
	if got := runner.skipped.Load(); got != 1 {
		t.Errorf("suppression counter = %d, want 1", got)
	}
	rounds := rec.rounds()
	if len(rounds) != 1 {
		t.Fatalf("expected one read round (the conditions), got %d", len(rounds))
	}
	if hasTag(rounds[0], "d1") || hasTag(rounds[0], "d2") {
		t.Errorf("details must not be requested while suppressed, round = %v", rounds[0])
	}
}

// TestTriggerGroupFollowsConditionChange: gating tracks the flag, it does not
// latch on the first evaluation.
func TestTriggerGroupFollowsConditionChange(t *testing.T) {
	rec := &triggerRecorder{values: map[string]any{"alarm": false}}
	runner := newTriggerRunner()

	if plan := runner.gateTriggerTags(context.Background(), runner.task, rec.read); len(plan.read) != 0 {
		t.Fatalf("false flag must suppress details, got %v", plan.read)
	}

	rec.set("alarm", true)
	plan := runner.gateTriggerTags(context.Background(), runner.task, rec.read)
	if !hasTag(plan.read, "d1") || !hasTag(plan.read, "d2") {
		t.Fatalf("true flag must release the details, got %v", plan.read)
	}
	// The condition was read on its own, never re-requested with the details.
	if hasTag(plan.read, "alarm") {
		t.Errorf("condition must not be re-requested in the detail pass, got %v", plan.read)
	}
	if len(plan.suppressed) != 0 {
		t.Errorf("a fired condition must suppress nothing, got %v", plan.suppressed)
	}
}

// TestTriggerFiredFailClosed pins the evaluation rules.
func TestTriggerFiredFailClosed(t *testing.T) {
	cases := []struct {
		name string
		v    core.TagValue
		want bool
	}{
		{"good bool true", core.TagValue{Value: true, Quality: core.QualityGood}, true},
		{"good bool false", core.TagValue{Value: false, Quality: core.QualityGood}, false},
		{"good numeric non-zero", core.TagValue{Value: int64(1), Quality: core.QualityGood}, true},
		{"good numeric zero", core.TagValue{Value: float32(0), Quality: core.QualityGood}, false},
		{"good string is not a flag", core.TagValue{Value: "true", Quality: core.QualityGood}, false},
		{"bad quality never fires", core.TagValue{Value: true, Quality: core.QualityBad}, false},
		{"uncertain quality never fires", core.TagValue{Value: true, Quality: core.QualityUncertain}, false},
		{"nil never fires", core.TagValue{Value: nil, Quality: core.QualityGood}, false},
	}
	for _, tc := range cases {
		if got := triggerFired(tc.v); got != tc.want {
			t.Errorf("%s: triggerFired = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTriggerGroupConditionReadErrorSuppressesAll: an unreachable flag cannot
// authorise a detail read; every gated tag is reported as suppressed.
func TestTriggerGroupConditionReadErrorSuppressesAll(t *testing.T) {
	runner := newTriggerRunner()
	failing := func(context.Context, string, []string) ([]core.TagValue, error) {
		return nil, context.DeadlineExceeded
	}
	plan := runner.gateTriggerTags(context.Background(), runner.task, failing)
	if len(plan.read) != 0 {
		t.Fatalf("a failed condition read must suppress the details, got %v", plan.read)
	}
	if !hasTag(plan.suppressed, "d1") || !hasTag(plan.suppressed, "d2") {
		t.Fatalf("suppressed details must still be reported, got %v", plan.suppressed)
	}
	if got := runner.skipped.Load(); got != 1 {
		t.Errorf("suppression counter = %d, want 1", got)
	}
}

// TestTriggerGroupNoGroupsReadsAllTags: a task without gating keeps the
// single-read path (task.Tags returned untouched, nothing suppressed).
func TestTriggerGroupNoGroupsReadsAllTags(t *testing.T) {
	runner := newTriggerRunner()
	runner.groupTrigger = nil
	runner.gated = nil
	plan := runner.gateTriggerTags(context.Background(), runner.task, nil)
	if len(plan.conditions) != 0 || len(plan.suppressed) != 0 {
		t.Errorf("ungated task must report nothing, got %+v", plan)
	}
	if len(plan.read) != len(runner.task.Tags) {
		t.Fatalf("ungated task must read all tags, got %v", plan.read)
	}
}

// TestSuppressedDetailsRepublishedAsUncertain proves the engine leaves a visible
// trace of a skipped detail point: the cached last-known-good value is pushed
// again with quality uncertain, and a never-collected detail stays absent
// rather than materialising as a fabricated zero.
func TestSuppressedDetailsRepublishedAsUncertain(t *testing.T) {
	e := newTestEngine(t)
	bus := NewDataBus(16)
	e.processing.dataBus = bus
	e.stats.readRate = metrics.NewRateWindow()
	e.processing.cache = NewLatestCache()
	e.processing.cache.Update(core.DataPoint{
		Driver: "plc", Tag: "d1", Value: float64(42), Type: core.TypeFloat32,
		Quality: core.QualityGood, Timestamp: time.Now().Add(-time.Minute),
	})

	lc := &lifecycleSupport{eng: e}
	plan := &triggerPlan{suppressed: []string{"d1", "d2"}}
	lc.onDriverData("plc", nil, plan, 0)

	select {
	case got := <-bus.Channel():
		if got.Tag != "d1" || got.Quality != core.QualityUncertain {
			t.Fatalf("republished point = %+v, want d1/uncertain", got)
		}
		if got.Value != float64(42) {
			t.Errorf("republished point must carry last-known-good value 42, got %v", got.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("no republished point: a skipped detail point must surface as uncertain")
	}

	// d2 was suppressed too but was never collected: nothing to republish.
	select {
	case extra := <-bus.Channel():
		t.Fatalf("a never-collected detail must stay absent, got %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}
