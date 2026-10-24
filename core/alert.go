package core

import (
	"time"
)

// Alert is the first-class record of a rule (or policy) firing on a
// data point: alerting must carry state, history, and an API of its
// own — a WARN log line alone would force the Dashboard to reconstruct
// an "alert" view by filtering log levels, mixing rule alerts with
// routine connection warnings.
//
// Identity is the (rule, driver, tag) triple: the same rule firing on
// the same tag updates the existing alert (bumping Count and
// LastFired) instead of creating duplicates.
type Alert struct {
	// Key is the stable identity "rule|driver|tag" used for ack and
	// stream correlation.
	Key        string    `json:"key"`
	Rule       string    `json:"rule"`
	Driver     string    `json:"driver"`
	Tag        string    `json:"tag"`
	Value      any       `json:"value,omitempty"`
	Quality    Quality   `json:"quality"`
	FirstFired time.Time `json:"first_fired"`
	LastFired  time.Time `json:"last_fired"`
	// Count is the number of fires while this alert has been active
	// (deduplicated by identity, not by window — every fire counts).
	Count uint64 `json:"count"`
	// Acked marks an operator-acknowledged alert. Acknowledgement sticks
	// until the alert clears (a full active-window without a re-fire);
	// a re-fire does not reopen it.
	Acked   bool      `json:"acked"`
	AckedAt time.Time `json:"acked_at,omitempty"`
}

// AlertEventType classifies the lifecycle transitions published on the
// alert stream (WS /alerts/stream).
type AlertEventType string

const (
	// AlertFired is emitted when a new alert becomes active.
	AlertFired AlertEventType = "fired"
	// AlertAcked is emitted when an operator acknowledges an alert.
	AlertAcked AlertEventType = "acked"
	// AlertCleared is emitted when an alert stops re-firing for a full
	// active-window and leaves the active set.
	AlertCleared AlertEventType = "cleared"
)

// AlertEvent is one alert lifecycle transition, the unit of the alert
// stream and the bounded history.
type AlertEvent struct {
	Type  AlertEventType `json:"type"`
	Alert Alert          `json:"alert"`
	At    time.Time      `json:"at"`
}

// AlertProvider exposes the engine's alert state for the control plane
// (GET /alerts, PATCH /alerts/{key}/ack). A separate role interface so
// the route layer type-asserts optionally; *CoreCEngine satisfies it,
// test mocks are free to omit it.
type AlertProvider interface {
	// ActiveAlerts returns the currently-active alerts (fired within
	// the active window, acknowledged or not), sorted oldest-first.
	ActiveAlerts() []Alert
	// AlertHistory returns the bounded recent alert events (fired /
	// acked / cleared), newest-first, capped by the engine.
	AlertHistory() []AlertEvent
	// AckAlert acknowledges the active alert with the given key.
	// Returns an error when no active alert carries that key.
	AckAlert(key string) error
}

// AlertStreamer exposes the real-time alert event stream for the
// WebSocket endpoint (WS /alerts/stream). Mirrors EventSubscriber for
// data points. *CoreCEngine satisfies it; test mocks are free to omit
// it.
type AlertStreamer interface {
	// SubscribeAlerts returns a channel of alert lifecycle events and
	// an unsubscribe function. Slow subscribers drop events (counted),
	// never blocking the processing pipeline.
	SubscribeAlerts() (ch <-chan AlertEvent, unsub func())
}

// AlertMetricsProvider exposes alert counters for Prometheus exposure:
// the cumulative number of fires per rule (rate() shows firing
// frequency) and the current number of active alerts. *CoreCEngine
// satisfies it; test mocks are free to omit it.
type AlertMetricsProvider interface {
	// AlertFireCounts returns the cumulative fire count per rule.
	AlertFireCounts() map[string]uint64
	// ActiveAlertCount returns the number of currently-active alerts.
	ActiveAlertCount() int
}
