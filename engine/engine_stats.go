package engine

import (
	"time"

	"github.com/CoreC-Dev/CoreC/common/metrics"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
)

func (e *CoreCEngine) Status() core.EngineStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status
}

// --- Rule Management ---

// --- Rule Management ---

func (e *CoreCEngine) SetRules(rules []core.RuleConfig) error {
	return e.ruleEngine.SetRules(rules)
}

func (e *CoreCEngine) GetRuleStats() []core.RuleStat {
	return e.ruleEngine.RuleStats()
}

func (e *CoreCEngine) SetRuleDisabled(index int, disabled bool) error {
	return e.ruleEngine.SetRuleDisabled(index, disabled)
}

// --- Stats ---

// --- Stats ---

func (e *CoreCEngine) Stats() core.EngineStats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	driverStats := make(map[string]core.DriverStatus)
	for name, d := range e.drivers {
		driverStats[name] = d.Status()
	}

	transportStats := make(map[string]core.TransportStatus)
	for name, t := range e.transports {
		transportStats[name] = t.Status()
	}

	uptime := time.Since(e.startTime)
	totalRead := e.totalRead.Load()
	pointsPerSec := float64(0)
	if uptime.Seconds() > 0 {
		pointsPerSec = float64(totalRead) / uptime.Seconds()
	}

	// dataBus is only created in Start(); before Start it is nil.
	// Report zero dropped counts instead of panicking on a nil receiver.
	var totalDropped uint64
	if e.dataBus != nil {
		totalDropped = uint64(e.dataBus.Dropped() + e.dataBus.PushDropped() + e.dataBus.HighPriorityPushDropped() + log.Dropped())
	}

	return core.EngineStats{
		Status:         e.status,
		Uptime:         uptime,
		Drivers:        len(e.drivers),
		Transports:     len(e.transports),
		Rules:          len(e.ruleEngine.Rules()),
		TotalRead:      totalRead,
		TotalPublish:   e.totalPublish.Load(),
		TotalErrors:    e.totalErrors.Load(),
		TotalDropped:   totalDropped,
		PointsPerSec:   pointsPerSec,
		DriverStats:    driverStats,
		TransportStats: transportStats,
	}
}

// OfflineBufferStats aggregates offline-buffer observability counters across
// all transport batchers for Prometheus exposure:
//   - pending: batches currently waiting on disk for replay (gauge).
//   - drained: total batches successfully replayed since startup (counter).
//   - pushed:  total batches persisted to the offline buffer since startup (counter).
//
// The offline buffer is a single shared instance (e.offlineBuffer) handed to
// every batcher in newTransportBatcher, so pending and drained are read once
// from it rather than summed per batcher — summing the per-batcher
// OfflineBufferPending/OfflineBufferDrained values would multiply the real
// count by the number of batchers, since they all delegate to the same
// buffer. pushed, by contrast, is a genuine per-batcher counter
// (offlineBufferPushes, incremented in publishWithRetryAndBuffer) and must be
// summed across batchers.
//
// The batchers map is snapshotted under RLock and then released before
// reading the counters, mirroring the Stop() pattern, so a slow Len() (disk
// readdir) can never block management API calls that take the write lock.
func (e *CoreCEngine) OfflineBufferStats() (pending int, drained, pushed uint64) {
	e.mu.RLock()
	batchers := make([]*transportBatcher, 0, len(e.batchers))
	for _, b := range e.batchers {
		batchers = append(batchers, b)
	}
	buf := e.offlineBuffer
	e.mu.RUnlock()

	// pushed is per-batcher: aggregate it.
	for _, b := range batchers {
		pushed += b.offlineBufferPushes.Load()
	}

	// pending and drained are properties of the shared buffer; read them
	// once to avoid double-counting across batchers.
	if buf != nil {
		pending = buf.PendingCount()
		drained = buf.DrainCount()
	}
	return pending, drained, pushed
}

// FlushBatchesDropped returns the total number of full batches that were
// evicted or dropped from batcher flush queues because the async flush
// consumer fell behind a sustained burst (IMPROVEMENTS #1: async batcher
// backpressure observability). It aggregates the per-batcher
// flushBatchesDropped counters. Intended for Prometheus exposure as
// corec_flush_batches_dropped_total.
func (e *CoreCEngine) FlushBatchesDropped() uint64 {
	e.mu.RLock()
	batchers := make([]*transportBatcher, 0, len(e.batchers))
	for _, b := range e.batchers {
		batchers = append(batchers, b)
	}
	e.mu.RUnlock()

	var total uint64
	for _, b := range batchers {
		total += b.flushBatchesDropped.Load()
	}
	return total
}

// onBatchPublish is the callback handed to each transportBatcher via
// newTransportBatcher. It is called with the number of points actually
// published when publishWithRetryAndBuffer or drainOnce succeeds — not when
// points are merely enqueued for async flush. This keeps totalPublish and
// the statistic manager's publish counter accurate for batched transports,
// where the real PublishBatch happens asynchronously after publish()
// returns (Finding 4 fix: previously publish.go counted totalPublish and
// statManager.PushPublish immediately after the near-instant enqueue, which
// inflated the publish counter and undercounted errors when batches later
// failed after retries).
func (e *CoreCEngine) onBatchPublish(n int) {
	e.totalPublish.Add(uint64(n))
	e.statManager.PushPublish(int64(n))
}

// ReadLatency returns the histogram tracking driver read latency
// (time spent in Driver.Read), for Prometheus histogram exposure. The
// returned pointer is always non-nil for an engine created via New().
func (e *CoreCEngine) ReadLatency() *metrics.LatencyHistogram {
	return e.readLatency
}

// ReadLatencyHistogram returns a point-in-time snapshot of the read
// latency histogram, satisfying the core.LatencyProvider interface.
func (e *CoreCEngine) ReadLatencyHistogram() core.LatencySnapshot {
	buckets, counts, sum, count := e.readLatency.Snapshot()
	return core.LatencySnapshot{Buckets: buckets, Counts: counts, Sum: sum, Count: count}
}

// PublishLatency returns the histogram tracking transport publish
// latency (time spent in Transport.Publish / batcher.publish, including
// fallback attempts), for Prometheus histogram exposure. The returned
// pointer is always non-nil for an engine created via New().
func (e *CoreCEngine) PublishLatency() *metrics.LatencyHistogram {
	return e.publishLatency
}

// PublishLatencyHistogram returns a point-in-time snapshot of the publish
// latency histogram, satisfying the core.LatencyProvider interface.
func (e *CoreCEngine) PublishLatencyHistogram() core.LatencySnapshot {
	buckets, counts, sum, count := e.publishLatency.Snapshot()
	return core.LatencySnapshot{Buckets: buckets, Counts: counts, Sum: sum, Count: count}
}

// DataAgeHistogram returns a point-in-time snapshot of the data-age
// (freshness) histogram, satisfying the core.DataAgeProvider interface.
// Data age is the elapsed time from DataPoint.Timestamp (collection/source
// time) to the publish attempt, measured at each publish site.
func (e *CoreCEngine) DataAgeHistogram() core.LatencySnapshot {
	buckets, counts, sum, count := e.dataAge.Snapshot()
	return core.LatencySnapshot{Buckets: buckets, Counts: counts, Sum: sum, Count: count}
}
