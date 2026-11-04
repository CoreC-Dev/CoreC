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

// check reports whether a write to driver may proceed now. On refusal the
// returned error wraps core.ErrWritePaced and names the remaining wait. On
// success the pacing slot is consumed (that driver's clock advances).
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
		return fmt.Errorf("%w: driver %q accepts a write every %s (retry in %s)",
			core.ErrWritePaced, driver, interval, wait.Round(time.Millisecond))
	}
	p.last[driver] = now
	return nil
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
