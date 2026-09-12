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
	RemoveTask(id string) error
	Pause() error
	Resume() error
	PauseDriver(name string) error
	ResumeDriver(name string) error
	Stats() SchedulerStats
}

// ScheduleTask defines a periodic collection task.
type ScheduleTask struct {
	ID        string             `json:"id"`
	Driver    string             `json:"driver"`
	Tags      []string           `json:"tags"`
	Interval  time.Duration      `json:"interval"`
	Priority  int                `json:"priority"`
	DeadBands map[string]float64 `json:"deadbands,omitempty"` // tag → deadband threshold
}

// SchedulerStats reports scheduler metrics.
type SchedulerStats struct {
	ActiveTasks  int     `json:"active_tasks"`
	PausedTasks  int     `json:"paused_tasks"`
	TickRate     float64 `json:"tick_rate"`
	Overruns     uint64  `json:"overruns"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}
