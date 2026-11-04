package engine

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// intervalDriver is a mock driver that declares an optional collection
// floor through the core.IntervalLimiter role.
type intervalDriver struct {
	*recordingDriver
	floor time.Duration
}

func (d *intervalDriver) MinInterval() time.Duration { return d.floor }

// newSchedulerForTest builds a running-less scheduler the scheduling code can
// hand tasks to, with no-op callbacks so the task goroutines cannot panic.
func newSchedulerForTest(t *testing.T) *scheduler {
	t.Helper()
	s := NewScheduler(
		func(context.Context, string, []string) ([]core.TagValue, error) { return nil, nil },
		func(string, []core.TagValue, *triggerPlan, int) {},
		0,
	)
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

// TestSchedulerClampsToDriverMinimumInterval: a tag asking for a cadence
// faster than the driver's declared floor is scheduled at the floor, not at
// the requested value — a 10 ms poll must never reach a driver that cannot
// sustain better than 50 ms.
func TestSchedulerClampsToDriverMinimumInterval(t *testing.T) {
	e := newTestEngine(t)
	e.lifecycle.scheduler = newSchedulerForTest(t)

	e.driverReg.drivers["slow"] = &intervalDriver{recordingDriver: newRecordingDriver("slow"), floor: 50 * time.Millisecond}
	e.driverReg.scheduleDriverTags(core.DriverConfig{
		Name: "slow",
		Tags: []core.TagConfig{{Name: "t1", Address: "1", Type: "int16", Interval: "10ms"}},
	})

	e.lifecycle.scheduler.mu.RLock()
	runner, ok := e.lifecycle.scheduler.tasks["slow_50ms"]
	e.lifecycle.scheduler.mu.RUnlock()
	if !ok {
		t.Fatalf("task not clamped: expected task ID slow_50ms, have %v", taskIDs(e.lifecycle.scheduler))
	}
	if runner.task.Interval != 50*time.Millisecond {
		t.Errorf("task interval = %v, want the driver floor %v", runner.task.Interval, 50*time.Millisecond)
	}
}

// TestSchedulerKeepsIntervalAboveDriverFloor: a floor shorter than the
// requested interval must not shorten it (fast driver, slow config is
// legitimate), and a driver without the role is entirely unaffected.
func TestSchedulerKeepsIntervalAboveDriverFloor(t *testing.T) {
	e := newTestEngine(t)
	e.lifecycle.scheduler = newSchedulerForTest(t)

	e.driverReg.drivers["slow"] = &intervalDriver{recordingDriver: newRecordingDriver("slow"), floor: 50 * time.Millisecond}
	e.driverReg.drivers["plain"] = newRecordingDriver("plain")

	e.driverReg.scheduleDriverTags(core.DriverConfig{
		Name: "slow",
		Tags: []core.TagConfig{{Name: "t1", Address: "1", Type: "int16", Interval: "1s"}},
	})
	e.driverReg.scheduleDriverTags(core.DriverConfig{
		Name: "plain",
		Tags: []core.TagConfig{{Name: "t1", Address: "1", Type: "int16", Interval: "10ms"}},
	})

	e.lifecycle.scheduler.mu.RLock()
	full, okFull := e.lifecycle.scheduler.tasks["slow_1s"]
	plain, okPlain := e.lifecycle.scheduler.tasks["plain_10ms"]
	e.lifecycle.scheduler.mu.RUnlock()

	if !okFull || full.task.Interval != time.Second {
		t.Errorf("slow requested interval must be left untouched, got ok=%v %+v", okFull, taskIDs(e.lifecycle.scheduler))
	}
	if !okPlain || plain.task.Interval != 10*time.Millisecond {
		t.Errorf("a driver without the IntervalLimiter role must keep its interval, got ok=%v %v", okPlain, taskIDs(e.lifecycle.scheduler))
	}
}

// TestDriverMinIntervalIgnoresNonPositive floors: a zero/negative floor meaning
// "no floor declared" must not clamp anything.
func TestDriverMinIntervalIgnoresNonPositive(t *testing.T) {
	if got := driverMinInterval(&intervalDriver{recordingDriver: newRecordingDriver("z"), floor: 0}); got != 0 {
		t.Errorf("zero floor = %v, want 0 (absent)", got)
	}
	if got := driverMinInterval(&intervalDriver{recordingDriver: newRecordingDriver("n"), floor: -time.Second}); got != 0 {
		t.Errorf("negative floor = %v, want 0 (absent)", got)
	}
	if got := driverMinInterval(newRecordingDriver("plain")); got != 0 {
		t.Errorf("driver without the role = %v, want 0 (absent)", got)
	}
}

// taskIDs returns the task IDs currently registered, for failure messages.
func taskIDs(s *scheduler) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.tasks))
	for id := range s.tasks {
		ids = append(ids, id)
	}
	return ids
}
