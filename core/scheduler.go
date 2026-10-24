package core

import (
	"time"
)

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

// TaskHealth reports the runtime health of one driver's scheduled
// collection tasks, aggregated across every task of that driver:
//
//   - Degraded: any task is currently auto-degraded (collection slowed
//     down after sustained read failures).
//   - ConsecutiveErrors: the worst streak of consecutive read failures
//     across the driver's tasks (resets on the first success).
//   - OverrunsTotal: cumulative count of reads whose latency exceeded
//     the collection interval. Monotonically increasing for the
//     process lifetime — never reset (the throttled log's windowed
//     counter is a separate field).
//
// This is the Prometheus-facing twin of the states the scheduler logs
// (auto-degrade, overrun warnings, consecutive read errors): log-only
// visibility means "why did this driver go quiet" cannot be answered
// by alerting.
type TaskHealth struct {
	Driver            string `json:"driver"`
	Degraded          bool   `json:"degraded"`
	ConsecutiveErrors int    `json:"consecutive_errors"`
	OverrunsTotal     uint64 `json:"overruns_total"`
}

// SchedulerHealthProvider is the optional role the metrics route
// type-asserts to expose scheduler health. Engines that do not
// implement it simply omit the scheduler metric families.
type SchedulerHealthProvider interface {
	// SchedulerHealth returns per-driver scheduler health, sorted by
	// driver name for deterministic exposition. nil/empty means no
	// scheduled tasks (idle engine).
	SchedulerHealth() []TaskHealth
}
