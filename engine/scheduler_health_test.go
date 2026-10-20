package engine

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// quietReadFunc is a read callback that never ticks during a test: the
// tasks below use hour-long intervals, so the scheduler goroutines sit
// idle on their tickers while Health() is exercised.
func quietReadFunc(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
	return nil, nil
}

func quietOnData(driver string, values []core.TagValue, priority int) {}

// TestSchedulerHealthAggregation pins the per-driver aggregation the
// metrics route exposes (audit OBS-5 / roadmap #7): degraded is
// any-task, consecutive errors are the worst streak, overruns are
// summed, and output is sorted by driver name for deterministic
// exposition.
func TestSchedulerHealthAggregation(t *testing.T) {
	s := NewScheduler(quietReadFunc, quietOnData, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("scheduler start failed: %v", err)
	}
	defer s.Stop()

	for _, task := range []core.ScheduleTask{
		{ID: "a_1s", Driver: "a", Tags: []string{"x"}, Interval: time.Hour},
		{ID: "a_2s", Driver: "a", Tags: []string{"z"}, Interval: 2 * time.Hour},
		{ID: "b_1s", Driver: "b", Tags: []string{"y"}, Interval: time.Hour},
	} {
		if err := s.AddTask(task); err != nil {
			t.Fatalf("AddTask(%s) failed: %v", task.ID, err)
		}
	}

	// Driver a: one degraded task with an error streak and overruns,
	// one healthy task with its own overruns; driver b: fully healthy.
	// The aggregation must not let the healthy task hide the streak.
	s.mu.Lock()
	for id, r := range s.tasks {
		switch id {
		case "a_1s":
			r.degraded = true
			r.consecutiveErrors = 3
			r.overrunsTotal = 5
		case "a_2s":
			r.overrunsTotal = 2
		}
	}
	s.mu.Unlock()

	got := s.Health()
	want := []core.TaskHealth{
		{Driver: "a", Degraded: true, ConsecutiveErrors: 3, OverrunsTotal: 7},
		{Driver: "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Health() = %+v, want %+v", got, want)
	}
}

// TestSchedulerHealthNilAndEmpty verifies the nil-scheduler guard (an
// engine that never started must not panic) and that a taskless
// scheduler reports no health rows rather than an empty family body.
func TestSchedulerHealthNilAndEmpty(t *testing.T) {
	var nilSched *scheduler
	if got := nilSched.Health(); got != nil {
		t.Errorf("nil scheduler Health() = %+v, want nil", got)
	}

	s := NewScheduler(quietReadFunc, quietOnData, 0, nil)
	if got := s.Health(); got != nil {
		t.Errorf("taskless scheduler Health() = %+v, want nil", got)
	}
}
