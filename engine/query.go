package engine

import (
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func (e *CoreCEngine) LatestValues(driver string) map[string]core.DataPoint {
	if driver == "" {
		// Aggregate every driver; this backs the GET /tags endpoint.
		return e.cache.GetAll()
	}
	return e.cache.GetByDriver(driver)
}

func (e *CoreCEngine) StaleThreshold() time.Duration {
	// Read atomically: this is called from HTTP handlers concurrently
	// with applyEngineConfig writes during Reload/Start.
	return time.Duration(e.staleThreshold.Load())
}

// --- Event Subscription ---

func (e *CoreCEngine) Subscribe(filter string) (ch <-chan core.DataPoint, unsub func()) {
	// dataBus is only created in Start(); before Start it is nil.
	// Return a nil channel and no-op unsubscribe instead of panicking.
	if e.dataBus == nil {
		return nil, func() {}
	}
	return e.dataBus.Subscribe(filter)
}

// SubscribeWithBuffer is like Subscribe but lets the caller choose the
// subscriber channel buffer size.
func (e *CoreCEngine) SubscribeWithBuffer(filter string, size int) (ch <-chan core.DataPoint, unsub func()) {
	// dataBus is only created in Start(); before Start it is nil.
	if e.dataBus == nil {
		return nil, func() {}
	}
	return e.dataBus.SubscribeWithBuffer(filter, size)
}

func (e *CoreCEngine) OnAlert(handler func(point core.DataPoint, rule core.Rule)) {
	e.mu.Lock()
	e.alertHandlers = append(e.alertHandlers, handler)
	e.mu.Unlock()
}
