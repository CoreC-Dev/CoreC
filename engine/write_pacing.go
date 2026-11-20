package engine

import (
	"fmt"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// writePacer limits the minimum spacing between two accepted writes to
// the same driver (engine-wide global.engine.write-min-interval floor).
// Pacing is per driver and non-blocking: a write arriving too soon is
// rejected with ErrWritePaced, never queued. Only record advances the clock.
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

// pacedError carries the remaining wait before the same driver accepts
// another write.
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
// check does not advance the clock (only record does). check-then-Write is
// not atomic: two concurrent writers may both be admitted inside the same
// gap.
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

// record advances driver's clock. Callers invoke it only after a write the
// device accepted.
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
