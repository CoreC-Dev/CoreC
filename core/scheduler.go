package core

import (
	"context"
	"time"
)

// Scheduler manages timed data collection tasks.
// This is unique to CoreC; the scheduler proactively polls on intervals.
type Scheduler interface {
	Start(ctx context.Context) error
	Stop() error
	AddTask(task ScheduleTask) error
	Pause() error
	Resume() error
	PauseDriver(name string) error
}

// ScheduleTask defines a periodic collection task.
type ScheduleTask struct {
	ID        string             `json:"id"`
	Driver    string             `json:"driver"`
	Tags      []string           `json:"tags"`
	Interval  time.Duration      `json:"interval"`
	Priority  int                `json:"priority"`
	DeadBands map[string]float64 `json:"deadbands,omitempty"` // tag → deadband threshold

	// ReadTimeout is the maximum duration for a single read operation.
	// <=0 means fall back to Interval. This decouples the read deadline
	// from the collection cadence so that high-frequency tasks (e.g.
	// 200 ms interval) can still allow a generous read timeout (e.g. 1 s)
	// without being cut short by network jitter.
	ReadTimeout time.Duration `json:"read_timeout,omitempty"`
}
