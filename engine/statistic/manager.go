package statistic

import (
	"runtime"
	"sync/atomic"
	"time"
)

// DefaultManager is the default global statistics manager.
var DefaultManager *Manager

func init() {
	DefaultManager = NewManager()
	go DefaultManager.handle()
}

// Manager collects and manages system statistics.
type Manager struct {
	readTemp     atomic.Int64
	readBlip     atomic.Int64
	publishTemp  atomic.Int64
	publishBlip  atomic.Int64
	readTotal    atomic.Int64
	publishTotal atomic.Int64
	errorTotal   atomic.Int64
	dropTotal    atomic.Int64
	startTime    time.Time
}

// NewManager creates a new Manager.
func NewManager() *Manager {
	return &Manager{
		startTime: time.Now(),
	}
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

// PushDrop increments the drop counter by count.
func (m *Manager) PushDrop(count int64) {
	m.dropTotal.Add(count)
}

// Now returns the current read and publish per-second rates.
func (m *Manager) Now() (read, publish int64) {
	return m.readBlip.Load(), m.publishBlip.Load()
}

// Total returns the total read and publish counts.
func (m *Manager) Total() (read, publish int64) {
	return m.readTotal.Load(), m.publishTotal.Load()
}

// Errors returns the total error count.
func (m *Manager) Errors() int64 {
	return m.errorTotal.Load()
}

// Drops returns the total dropped message count (slow subscribers).
func (m *Manager) Drops() int64 {
	return m.dropTotal.Load()
}

// Uptime returns the duration since the manager started.
func (m *Manager) Uptime() time.Duration {
	return time.Since(m.startTime)
}

// Memory returns the current memory allocation.
func (m *Manager) Memory() uint64 {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return mem.Alloc
}

// Snapshot returns a snapshot of current statistics.
type Snapshot struct {
	ReadTotal     int64         `json:"read_total"`
	PublishTotal  int64         `json:"publish_total"`
	ErrorTotal    int64         `json:"error_total"`
	DropTotal     int64         `json:"drop_total"`
	ReadPerSec    int64         `json:"read_per_sec"`
	PublishPerSec int64         `json:"publish_per_sec"`
	Uptime        time.Duration `json:"uptime"`
	Memory        uint64        `json:"memory"`
}

// Snapshot generates a new snapshot struct of current stats.
func (m *Manager) Snapshot() *Snapshot {
	return &Snapshot{
		ReadTotal:     m.readTotal.Load(),
		PublishTotal:  m.publishTotal.Load(),
		ErrorTotal:    m.errorTotal.Load(),
		DropTotal:     m.dropTotal.Load(),
		ReadPerSec:    m.readBlip.Load(),
		PublishPerSec: m.publishBlip.Load(),
		Uptime:        m.Uptime(),
		Memory:        m.Memory(),
	}
}

// ResetStatistic resets the total counters.
func (m *Manager) ResetStatistic() {
	m.readTotal.Store(0)
	m.publishTotal.Store(0)
	m.errorTotal.Store(0)
	m.dropTotal.Store(0)
}

// handle periodically updates the snapshot rates (per second).
func (m *Manager) handle() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.readBlip.Store(m.readTemp.Swap(0))
		m.publishBlip.Store(m.publishTemp.Swap(0))
	}
}
