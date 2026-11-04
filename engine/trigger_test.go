package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

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

func hasTag(r []string, tag string) bool {
	for _, x := range r {
		if x == tag {
			return true
		}
	}
	return false
}

// TestTriggerGroupSkipsDetailsWhenConditionFalse is the core contract: while
// the condition is false the detail tags are never requested, the condition
// itself still is (consumers must see the flag change).
func TestTriggerGroupSkipsDetailsWhenConditionFalse(t *testing.T) {
	rec := &triggerRecorder{values: map[string]any{"alarm": false}}
	runner := newTriggerRunner()

	readTags, cond := runner.gateTriggerTags(context.Background(), runner.task, rec.read)
	if len(cond) != 1 || cond[0].Tag != "alarm" {
		t.Fatalf("condition read returned %+v, want the alarm tag", cond)
	}
	if len(readTags) != 0 {
		t.Fatalf("detail tags must be skipped while the condition is false, got %v", readTags)
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

// TestTriggerGroupFollowsConditionChange: the same runner collects nothing
// while the flag is false and collects the details once it flips true --
// gating must track the flag, not latch on the first evaluation.
func TestTriggerGroupFollowsConditionChange(t *testing.T) {
	rec := &triggerRecorder{values: map[string]any{"alarm": false}}
	runner := newTriggerRunner()

	if tags, _ := runner.gateTriggerTags(context.Background(), runner.task, rec.read); len(tags) != 0 {
		t.Fatalf("false flag must suppress details, got %v", tags)
	}
	rec.set("alarm", true)
	tags, _ := runner.gateTriggerTags(context.Background(), runner.task, rec.read)
	if !hasTag(tags, "d1") || !hasTag(tags, "d2") {
		t.Fatalf("true flag must release the details, got %v", tags)
	}
}

// TestTriggerGroupReadsDetailsWhenConditionTrue: a true condition yields the
// gated tags and never re-lists the condition (it was already read).
func TestTriggerGroupReadsDetailsWhenConditionTrue(t *testing.T) {
	rec := &triggerRecorder{values: map[string]any{"alarm": true}}
	runner := newTriggerRunner()

	readTags, _ := runner.gateTriggerTags(context.Background(), runner.task, rec.read)
	if !hasTag(readTags, "d1") || !hasTag(readTags, "d2") {
		t.Fatalf("both details must be read when the condition fired, got %v", readTags)
	}
	if hasTag(readTags, "alarm") {
		t.Errorf("the condition must not be re-requested in the detail pass, got %v", readTags)
	}
	if runner.skipped.Load() != 0 {
		t.Errorf("a fired condition must not count as a suppression")
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
// authorize a detail read; every gated tag stays out of the round.
func TestTriggerGroupConditionReadErrorSuppressesAll(t *testing.T) {
	runner := newTriggerRunner()
	failing := func(context.Context, string, []string) ([]core.TagValue, error) {
		return nil, context.DeadlineExceeded
	}
	readTags, _ := runner.gateTriggerTags(context.Background(), runner.task, failing)
	if len(readTags) != 0 {
		t.Fatalf("a failed condition read must suppress the details, got %v", readTags)
	}
	if got := runner.skipped.Load(); got != 1 {
		t.Errorf("suppression counter = %d, want 1", got)
	}
}

// TestTriggerGroupNoGroupsReadsAllTags: a task without gating keeps the
// single-read path (task.Tags returned untouched).
func TestTriggerGroupNoGroupsReadsAllTags(t *testing.T) {
	runner := newTriggerRunner()
	runner.groupTrigger = nil
	runner.gated = nil
	readTags, cond := runner.gateTriggerTags(context.Background(), runner.task, nil)
	if len(cond) != 0 {
		t.Errorf("no conditions expected, got %v", cond)
	}
	if len(readTags) != len(runner.task.Tags) {
		t.Fatalf("ungated task must read all tags, got %v", readTags)
	}
}
