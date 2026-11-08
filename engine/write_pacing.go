package engine

import (
	"fmt"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// writePacer bounds how often one driver may be written, using the
// engine-wide global.engine.write-min-interval floor: the minimum time
// between two write commands that actually reach the same device. Old PLCs
// that service their communication interface from a single CPU loop drop or
// misapply back-to-back commands, and pacing per tag in a rule scene can
// still collapse into a burst when one batch carries writes for several tags
// of the same device.
//
// Pacing is per driver (each device's last write is tracked independently)
// and non-blocking: a write arriving too soon is rejected, never queued.
// Queueing would only shift the burst to the next slot and would let a
// caller pay unbounded latency for a control action.
//
// Only accepted writes advance the clock, so a failed write does not consume
// the next writer's turn.
type writePacer struct {
	mu       sync.Mutex
	last     map[string]time.Time
	interval time.Duration
}

func newWritePacer() *writePacer {
	return &writePacer{last: make(map[string]time.Time)}
}

// setInterval configures the engine-wide spacing floor. <= 0 disables pacing
// and drops the per-driver clock map.
func (p *writePacer) setInterval(interval time.Duration) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interval = interval
	if interval <= 0 {
		p.last = make(map[string]time.Time)
	}
}

// currentInterval returns the configured floor (0 = disabled).
func (p *writePacer) currentInterval() time.Duration {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interval
}

// pacedError carries the remaining wait so the command retry loop can sleep
// exactly the device window instead of a generic exponential backoff.
type pacedError struct {
	driver   string
	interval time.Duration
	wait     time.Duration
}

func (e *pacedError) Error() string {
	return fmt.Sprintf("%s: driver %q accepts a write every %s (retry in %s)",
		core.ErrWritePaced, e.driver, e.interval, e.wait.Round(time.Millisecond))
}

func (e *pacedError) Unwrap() error { return core.ErrWritePaced }

// RetryAfter reports how long the caller must wait before the same write can
// be admitted.
func (e *pacedError) RetryAfter() time.Duration { return e.wait }

// check reports whether a write to driver may proceed now. On refusal the
// returned error wraps core.ErrWritePaced and names the remaining wait.
//
// It does NOT consume the slot. A check is a question, and the same question has
// to be askable again if the write it precedes turns out to fail: the docs promise
// 失败写入不占用下一次机会, and the old version of this function broke that promise
// by advancing the clock here, before the driver had been asked. Advancing is now
// a separate act (record), taken only once the device has accepted the write.
//
// Consequently check-then-Write is not atomic between two concurrent writers: both
// may be admitted inside the same gap and the device may see them back to back.
// That is the honest shape of the guarantee -- pacing bounds the spacing of
// ACCEPTED writes, not the admission of attempts -- and it is the same race the
// driver-side floor protects against.
func (p *writePacer) check(driver string) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	interval := p.interval
	if interval <= 0 {
		return nil
	}
	prev, ok := p.last[driver]
	now := time.Now()
	if ok && now.Sub(prev) < interval {
		wait := interval - now.Sub(prev)
		return &pacedError{driver: driver, interval: interval, wait: wait}
	}
	return nil
}

// record advances driver's clock after a write the device ACCEPTED.
//
// Callers that pass check must reach this only when the write succeeded. A
// refused or errored write spent nothing at the device and must leave the window
// open, otherwise the next attempt waits a full interval to learn the same thing.
//
// The timestamp is taken here rather than inside check so that the spacing is
// measured between accepted writes; taking it at admission would let the gap
// between admission and acceptance count against the next writer.
func (p *writePacer) record(driver string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.interval <= 0 {
		return
	}
	p.last[driver] = time.Now()
}

// forget drops the clock for a driver (driver removed or reconfigured).
func (p *writePacer) forget(driver string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.last, driver)
}
