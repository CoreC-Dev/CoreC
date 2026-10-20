// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/observable"
	"github.com/CoreC-Dev/CoreC/core"
)

// Alert registry capacities. The active registry is bounded to keep a
// pathological rule (e.g. matching every tag of a 10k-point PLC) from
// growing the map without limit: when full, the oldest-fired alerts
// are cleared first. History is a bounded ring, newest last.
const (
	alertRegistryCap = 1000
	alertHistoryCap  = 500

	// alertEventBufferSize bounds the source channel feeding the alert
	// event fan-out. Fire happens on the processing hot path, so an
	// event publish must never block: when the buffer is full the event
	// is dropped and counted by the Observable's drop counter.
	alertEventBufferSize = 256
)

// alertManager is the engine's first-class alert state (audit findings
// OBS-4/HUB-6): alerts previously degenerated to a WARN log line that
// the Dashboard had to reconstruct by filtering log levels. The manager
// gives alerts identity (rule|driver|tag), lifecycle (fired → acked →
// cleared), a bounded event history, and a real-time event stream for
// the /alerts API family.
//
// Model (registered in the production-hardening exec plan): no recovery
// expressions — an alert is "active" while it keeps re-firing within the
// active window, and clears (to history) after a full window of
// silence. Acknowledgement sticks until the alert clears: a re-fire
// does not reopen an acked alert (no ack ping-pong).
type alertManager struct {
	mu     sync.Mutex
	active map[string]*core.Alert

	// firedByRule counts every fire per rule for the
	// corec_alerts_total{rule} counter. Raw (not deduplicated) so
	// rate() reflects true firing frequency.
	firedByRule map[string]uint64

	// history is the bounded ring of lifecycle events, oldest first.
	history []core.AlertEvent

	// activeWindow is how long an alert survives after its last fire.
	// Defaults to core.DefaultAlertActiveWindow; the engine config can
	// override it (global.engine.alert-active-window).
	activeWindow time.Duration

	// events fans out lifecycle transitions to WS subscribers. The
	// source channel is buffered; Record never blocks on it.
	eventCh  chan core.AlertEvent
	eventBus *observable.Observable[core.AlertEvent]

	// closed marks the event channel closed at shutdown so a late
	// record drops its event instead of panicking (mirrors DataBus.Push
	// semantics; after the engine joins its workers no record can
	// happen, this is defense-in-depth).
	closed atomic.Bool

	// wg tracks the sweep goroutine; cancel stops it.
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

func newAlertManager(activeWindow time.Duration) *alertManager {
	if activeWindow <= 0 {
		activeWindow = core.DefaultAlertActiveWindow
	}
	am := &alertManager{
		active:       make(map[string]*core.Alert),
		firedByRule:  make(map[string]uint64),
		activeWindow: activeWindow,
		eventCh:      make(chan core.AlertEvent, alertEventBufferSize),
	}
	am.eventBus = observable.NewObservable[core.AlertEvent](am.eventCh)
	return am
}

// alertKey builds the stable identity from a fired point + rule.
func alertKey(rule core.Rule, driver, tag string) string {
	return rule.Name() + "|" + driver + "|" + tag
}

// start launches the sweep goroutine that clears alerts after a full
// active window of silence (emitting cleared events) and caps the
// registry size. Sweep cadence is window/4 (at least every 30s), so an
// expired alert leaves the active set within ~window/4 of its expiry.
func (am *alertManager) start(ctx context.Context) {
	sweepEvery := am.activeWindow / 4
	if sweepEvery < 30*time.Second {
		sweepEvery = 30 * time.Second
	}
	sweepEvery = minDuration(sweepEvery, am.activeWindow)
	ctx, am.cancel = context.WithCancel(ctx)
	am.wg.Add(1)
	go func() {
		defer am.wg.Done()
		ticker := time.NewTicker(sweepEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				am.sweep(time.Now())
			}
		}
	}()
}

// stop cancels the sweep goroutine, waits for it, then closes the event
// source so the fan-out goroutine terminates and every subscriber
// channel is closed (WS handlers see the closure and end). Safe only
// after every goroutine that can call record() has been joined — the
// engine calls this after its engine-wide wait group.
func (am *alertManager) stop() {
	if am.cancel != nil {
		am.cancel()
	}
	am.wg.Wait()
	// Idempotent close: a second stop (e.g. the double-Stop path) is
	// a no-op instead of panicking on close of a closed channel.
	if am.closed.CompareAndSwap(false, true) {
		close(am.eventCh)
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// record registers a fire of the given rule on the given point,
// creating or updating the alert with identity rule|driver|tag. It is
// called from the processing hot path and never blocks: the event
// channel publish is a select with default.
//
// A NEW alert emits a "fired" event; subsequent fires only bump
// Count/LastFired (stream consumers get state changes, not a flood of
// duplicates — the dedup lives in the active-window itself, and the
// /alerts view shows the aggregated Count).
func (am *alertManager) record(rule core.Rule, point core.DataPoint) {
	key := alertKey(rule, point.Driver, point.Tag)
	now := time.Now()

	am.mu.Lock()
	am.firedByRule[rule.Name()]++

	a, ok := am.active[key]
	var ev *core.AlertEvent
	if !ok {
		a = &core.Alert{
			Key:        key,
			Rule:       rule.Name(),
			Driver:     point.Driver,
			Tag:        point.Tag,
			Value:      point.Value,
			Quality:    point.Quality,
			FirstFired: now,
		}
		am.active[key] = a
		ev = &core.AlertEvent{Type: core.AlertFired, Alert: *a, At: now}
	}
	a.Value = point.Value
	a.Quality = point.Quality
	a.LastFired = now
	a.Count++

	// Registry cap: under a pathological rule (matching every tag), the
	// oldest-fired alerts are cleared first rather than growing the map
	// without limit.
	if len(am.active) > alertRegistryCap {
		am.evictOldestLocked(now)
	}
	if ev != nil {
		am.appendHistoryLocked(*ev)
	}
	am.mu.Unlock()

	if ev != nil {
		am.publish(*ev)
	}
}

// evictOldestLocked clears just enough oldest-fired alerts to bring
// the registry back under its cap. Caller holds am.mu.
func (am *alertManager) evictOldestLocked(now time.Time) {
	byLastFired := make([]*core.Alert, 0, len(am.active))
	for _, a := range am.active {
		byLastFired = append(byLastFired, a)
	}
	sort.Slice(byLastFired, func(i, j int) bool {
		return byLastFired[i].LastFired.Before(byLastFired[j].LastFired)
	})
	for _, a := range byLastFired[:len(am.active)-alertRegistryCap] {
		delete(am.active, a.Key)
		ev := core.AlertEvent{Type: core.AlertCleared, Alert: *a, At: now}
		am.appendHistoryLocked(ev)
		am.publish(ev)
	}
}

// appendHistoryLocked appends an event to the bounded history ring.
// Caller holds am.mu.
func (am *alertManager) appendHistoryLocked(ev core.AlertEvent) {
	am.history = append(am.history, ev)
	if len(am.history) > alertHistoryCap {
		am.history = am.history[len(am.history)-alertHistoryCap:]
	}
}

// publish fans an event out to stream subscribers without ever
// blocking the caller: a full source buffer drops the event (the
// Observable's drop counter makes the loss visible). A closed channel
// (shutdown) drops the event too — the recover mirrors DataBus.Push's
// closed-race guard.
func (am *alertManager) publish(ev core.AlertEvent) {
	if am.closed.Load() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			// The only expected panic is send-on-closed-channel from
			// the tiny race between closed.Store and this send; drop
			// the event. Any other panic indicates a real bug — but at
			// this point the engine is shutting down, so dropping is
			// the safe terminal behavior either way.
			_ = r
		}
	}()
	select {
	case am.eventCh <- ev:
	default:
	}
}

// ack acknowledges the active alert with the given key. The alert
// stays in the active set (still visible, marked acked) and the ack
// sticks until the alert clears.
func (am *alertManager) ack(key string) error {
	now := time.Now()
	am.mu.Lock()
	a, ok := am.active[key]
	if !ok {
		am.mu.Unlock()
		return fmt.Errorf("no active alert with key %q", key)
	}
	a.Acked = true
	a.AckedAt = now
	ev := core.AlertEvent{Type: core.AlertAcked, Alert: *a, At: now}
	am.appendHistoryLocked(ev)
	am.mu.Unlock()

	am.publish(ev)
	return nil
}

// sweep clears alerts whose last fire is older than the active window
// and is called by the sweep ticker. Emits one cleared event per alert.
func (am *alertManager) sweep(now time.Time) {
	am.mu.Lock()
	var cleared []core.AlertEvent
	for key, a := range am.active {
		if now.Sub(a.LastFired) >= am.activeWindow {
			delete(am.active, key)
			ev := core.AlertEvent{Type: core.AlertCleared, Alert: *a, At: now}
			am.appendHistoryLocked(ev)
			cleared = append(cleared, ev)
		}
	}
	am.mu.Unlock()
	for _, ev := range cleared {
		am.publish(ev)
	}
}

// activeAlerts returns the current alerts sorted oldest-first (by
// FirstFired), the natural order for an operations list.
func (am *alertManager) activeAlerts() []core.Alert {
	am.mu.Lock()
	defer am.mu.Unlock()
	out := make([]core.Alert, 0, len(am.active))
	for _, a := range am.active {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FirstFired.Before(out[j].FirstFired)
	})
	return out
}

// alertHistory returns the bounded recent events newest-first.
func (am *alertManager) alertHistory() []core.AlertEvent {
	am.mu.Lock()
	defer am.mu.Unlock()
	out := make([]core.AlertEvent, len(am.history))
	// Index rather than range-copy (gocritic rangeValCopy).
	for i := range am.history {
		out[len(am.history)-1-i] = am.history[i]
	}
	return out
}

// fireCounts returns a copy of the per-rule cumulative fire counts.
func (am *alertManager) fireCounts() map[string]uint64 {
	am.mu.Lock()
	defer am.mu.Unlock()
	out := make(map[string]uint64, len(am.firedByRule))
	for k, v := range am.firedByRule {
		out[k] = v
	}
	return out
}

// activeCount returns the number of active alerts.
func (am *alertManager) activeCount() int {
	am.mu.Lock()
	defer am.mu.Unlock()
	return len(am.active)
}

// subscribe returns the alert event stream (WS /alerts/stream).
func (am *alertManager) subscribe() (<-chan core.AlertEvent, func()) {
	return am.eventBus.Subscribe()
}
