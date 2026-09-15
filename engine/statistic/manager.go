// Package statistic collects engine throughput counters (reads,
// publishes, errors) as atomic counters.
package statistic

import (
	"sync/atomic"
)

// DefaultManager is the default global statistics manager. It is a
// process-global counter sink written to by the engine, scheduler, and
// drivers via PushRead/PushPublish/PushError. The API layer reads its
// rates from engine.EngineStats, not from this package.
var DefaultManager = NewManager()

// Manager collects read/publish/error counters.
type Manager struct {
	readTemp     atomic.Int64
	publishTemp  atomic.Int64
	readTotal    atomic.Int64
	publishTotal atomic.Int64
	errorTotal   atomic.Int64
}

// NewManager creates a new Manager.
func NewManager() *Manager {
	return &Manager{}
}

// PushRead increments the read counter.
func (m *Manager) PushRead(count int64) {
	m.readTemp.Add(count)
	m.readTotal.Add(count)
}

// PushPublish increments the publish counter.
func (m *Manager) PushPublish(count int64) {
	m.publishTemp.Add(count)
	m.publishTotal.Add(count)
}

// PushError increments the error counter.
func (m *Manager) PushError() {
	m.errorTotal.Add(1)
}

// Snapshot is a point-in-time view of the counters.
type Snapshot struct {
	ReadTotal    int64 `json:"read_total"`
	PublishTotal int64 `json:"publish_total"`
	ErrorTotal   int64 `json:"error_total"`
}

// Snapshot returns a snapshot of the current counters.
func (m *Manager) Snapshot() Snapshot {
	return Snapshot{
		ReadTotal:    m.readTotal.Load(),
		PublishTotal: m.publishTotal.Load(),
		ErrorTotal:   m.errorTotal.Load(),
	}
}
