package engine

import (
	"context"
	"reflect"
	"sync"
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
// metrics route exposes: degraded is
// any-task, consecutive errors are the worst streak, overruns are
// summed, and output is sorted by driver name for deterministic
// exposition.
func TestSchedulerHealthAggregation(t *testing.T) {
	s := NewScheduler(quietReadFunc, quietOnData, 0)
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

	s := NewScheduler(quietReadFunc, quietOnData, 0)
	if got := s.Health(); got != nil {
		t.Errorf("taskless scheduler Health() = %+v, want nil", got)
	}
}

// TestSchedulerPauseResumeRoundTrip verifies the pause/resume semantics
// the API exposes: paused drivers stop
// collecting, resume restores the cadence, and PausedDrivers reports
// the set.
func TestSchedulerPauseResumeRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var readsByDriver = make(map[string]int)
	readFn := func(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
		mu.Lock()
		readsByDriver[driver]++
		mu.Unlock()
		return []core.TagValue{{Tag: tags[0], Value: 1, Timestamp: time.Now()}}, nil
	}
	s := NewScheduler(readFn, func(driver string, values []core.TagValue, priority int) {},
		0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("scheduler start: %v", err)
	}
	defer s.Stop()

	const fastInterval = 10 * time.Millisecond
	for _, d := range []string{"a", "b"} {
		if err := s.AddTask(core.ScheduleTask{
			ID: d + "_t", Driver: d, Tags: []string{"x"}, Interval: fastInterval,
		}); err != nil {
			t.Fatalf("AddTask(%s): %v", d, err)
		}
	}

	// Baseline: both drivers collect.
	snapshotReads := func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		cp := make(map[string]int, len(readsByDriver))
		for k, v := range readsByDriver {
			cp[k] = v
		}
		return cp
	}
	waitFor := func(cond func() bool, window time.Duration, msg string) {
		t.Helper()
		deadline := time.Now().Add(window)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out: %s (reads=%v)", msg, snapshotReads())
			}
			time.Sleep(fastInterval)
		}
	}
	waitFor(func() bool { return snapshotReads()["a"] >= 2 && snapshotReads()["b"] >= 2 },
		2*time.Second, "both drivers should collect")

	// Pause a: its reads freeze while b keeps going.
	if err := s.PauseDriver("a"); err != nil {
		t.Fatalf("PauseDriver: %v", err)
	}
	if paused := s.PausedDrivers(); len(paused) != 1 || paused[0] != "a" {
		t.Fatalf("PausedDrivers = %v, want [a]", paused)
	}
	before := snapshotReads()["a"]
	waitFor(func() bool { return snapshotReads()["b"] >= before+2 },
		2*time.Second, "b must keep collecting while a is paused")
	if got := snapshotReads()["a"]; got != before {
		t.Errorf("paused driver collected: %d -> %d reads", before, got)
	}

	// Resume a: collection restarts at the next tick.
	if err := s.ResumeDriver("a"); err != nil {
		t.Fatalf("ResumeDriver: %v", err)
	}
	if paused := s.PausedDrivers(); len(paused) != 0 {
		t.Fatalf("PausedDrivers after resume = %v, want empty", paused)
	}
	resumedAt := snapshotReads()["a"]
	waitFor(func() bool { return snapshotReads()["a"] >= resumedAt+2 },
		2*time.Second, "resumed driver must collect again")
}
