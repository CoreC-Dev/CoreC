package mqtt

import (
	"sync"
	"time"
)

// replayWindow is a bounded, time-expiring set of seen message hashes.
// It prevents true replay attacks (same message accepted twice) within
// the timestamp skew window. Entries older than the TTL are evicted
// lazily on each check and periodically via a background goroutine.
type replayWindow struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	ttl     time.Duration
	maxSize int
	stop    chan struct{}
}

func newReplayWindow(ttl time.Duration, maxSize int) *replayWindow {
	rw := &replayWindow{
		seen:    make(map[string]time.Time),
		ttl:     ttl,
		maxSize: maxSize,
		stop:    make(chan struct{}),
	}
	if ttl > 0 {
		go rw.evictLoop()
	}
	return rw
}

// close stops the background eviction goroutine. It is safe to call
// multiple times (subsequent calls are no-ops). The publisher calls
// this in Stop to prevent the eviction goroutine from outliving the
// transport.
func (rw *replayWindow) close() {
	select {
	case <-rw.stop:
		// already closed
	default:
		close(rw.stop)
	}
}

func (rw *replayWindow) evictLoop() {
	ticker := time.NewTicker(rw.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-rw.stop:
			return
		case <-ticker.C:
			rw.evictExpired()
		}
	}
}

func (rw *replayWindow) evictExpired() {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	now := time.Now()
	for k, t := range rw.seen {
		if now.Sub(t) > rw.ttl {
			delete(rw.seen, k)
		}
	}
}

// checkAndAdd returns true if the hash is new (not seen before), false if
// it is a replay (already seen within the TTL window). It atomically adds
// the hash to the set if it is new.
func (rw *replayWindow) checkAndAdd(hash string) bool {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	now := time.Now()
	if _, ok := rw.seen[hash]; ok {
		return false // replay
	}
	// Bound the cache size: if at capacity, evict oldest entries.
	if len(rw.seen) >= rw.maxSize {
		var oldestKey string
		var oldestTime time.Time
		for k, t := range rw.seen {
			if oldestKey == "" || t.Before(oldestTime) {
				oldestKey = k
				oldestTime = t
			}
		}
		delete(rw.seen, oldestKey)
	}
	rw.seen[hash] = now
	return true
}
