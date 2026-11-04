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

	// EnforceIntervals are per-tag heartbeat periods on top of the deadband:
	// when a value has been suppressed as unchanged for longer than the
	// tag's period, the next sample is released anyway so a consumer can
	// tell "no change" apart from "link dead" (upsert/heartbeat pattern).
	// Only meaningful for tags that also declare a deadband; absent map =
	// no heartbeats (deadband suppresses unchanged values indefinitely).
	EnforceIntervals map[string]time.Duration `json:"enforce_intervals,omitempty"`

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
	// HeartbeatsTotal is the number of samples released because a tag's
	// enforce-interval heartbeat elapsed while the value stayed inside its
	// deadband. Exposed as corec_deadband_heartbeats_total{driver}: a
	// rising counter means the system is publishing unchanged values to
	// keep the consumer's last-known-value fresh, not that data changed.
	HeartbeatsTotal uint64 `json:"heartbeats_total,omitempty"`
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

// IntervalLimiter is the optional role a Driver implements to declare the
// fastest collection cadence its protocol/device can sustain (e.g. a Modbus
// RTU device at 9600 baud cannot answer 10 ms polls). The engine clamps a
// task's interval up to this floor at scheduling time and logs one warning
// naming the tag, the requested interval and the floor — a configuration that
// would silently overload the device fails loudly instead.
//
// It is a separate optional interface, not a DriverCapabilities field, so
// existing drivers and third-party drivers are unaffected: a driver that does
// not implement it keeps whatever interval the operator configured.
type IntervalLimiter interface {
	// MinInterval returns the driver's minimum sustainable collection
	// interval. <= 0 means "no floor declared" and is treated as absent.
	MinInterval() time.Duration
}
