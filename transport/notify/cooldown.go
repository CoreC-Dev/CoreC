package notify

import (
	"sync"
	"time"
)

// cooldownTracker suppresses repeated notifications for the same rule key
// within a configurable window. This prevents high-frequency tag updates
// from flooding alert channels (e.g. a value oscillating around a threshold
// at 200ms intervals).
type cooldownTracker struct {
	mu       sync.Mutex
	window   time.Duration
	lastSent map[string]time.Time
}

// newCooldownTracker creates a tracker with the given suppression window.
// A zero or negative window disables suppression (allow always returns true).
func newCooldownTracker(window time.Duration) *cooldownTracker {
	return &cooldownTracker{
		window:   window,
		lastSent: make(map[string]time.Time),
	}
}

// allow returns true if the rule key may send now (i.e. no send has occurred
// for this key within the window, or suppression is disabled). When true is
// returned, the key's last-sent time is updated to now.
func (c *cooldownTracker) allow(key string) bool {
	if c == nil || c.window <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	last, exists := c.lastSent[key]
	if exists && now.Sub(last) < c.window {
		return false
	}
	c.lastSent[key] = now
	return true
}
