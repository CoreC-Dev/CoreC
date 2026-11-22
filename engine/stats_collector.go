package engine

import (
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/metrics"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
)

// statsCollector groups the engine's observability state: throughput
// counters (reads/publishes/errors), the uptime anchor, the read/publish/
// data-age latency histograms, and the per-engine statistic manager.
// CoreCEngine keeps thin forwarders to this role so the
// core.StatsProvider,
// core.LatencyProvider, core.DataAgeProvider, and the optional
// offline-buffer/flush-drop provider method sets are unchanged.
type statsCollector struct {
	totalRead    atomic.Uint64
	totalPublish atomic.Uint64
	totalErrors  atomic.Uint64
	startTime    time.Time

	// driverMetrics is the registry drivers emit protocol-level
	// metrics into via the core.Metrics port.
	driverMetrics *metrics.Registry

	// badQualityDropped counts points discarded by the
	// on-bad-quality: drop policy, separately from totalErrors.
	badQualityDropped atomic.Uint64

	// readRate tracks points-per-second over the last minute so
	// EngineStats.PointsPerSec reports a current windowed rate.
	readRate *metrics.RateWindow

	// labeled owns every latency histogram in the engine: the global
	// read/publish/data-age aggregates AND the per-driver /
	// per-transport labeled histograms.
	labeled *labeledLatencyStore

	// eng is the owning engine: Stats reads the component maps and
	// status through it; the offline-buffer and flush-drop aggregators
	// reach the batcher registry through it.
	eng *CoreCEngine
}

func (s *statsCollector) Stats() core.EngineStats {
	s.eng.mu.RLock()
	defer s.eng.mu.RUnlock()

	driverStats := make(map[string]core.DriverStatus)
	for name, d := range s.eng.driverReg.drivers {
		driverStats[name] = d.Status()
	}

	transportStats := make(map[string]core.TransportStatus)
	for name, t := range s.eng.transportMgr.transports {
		transportStats[name] = t.Status()
	}

	uptime := time.Since(s.startTime)
	totalRead := s.totalRead.Load()

	// Current rate over the last-minute window. For an engine younger
	// than the window, fall back to the uptime as the divisor, floored
	// at one second.
	pointsPerSec := float64(0)
	windowSecs := s.readRate.WindowSeconds()
	elapsedSecs := int64(uptime.Seconds())
	divisor := max(min(elapsedSecs, windowSecs), 1)
	pointsPerSec = float64(s.readRate.Sum(time.Now())) / float64(divisor)

	// dataBus is only created in Start(); before Start it is nil.
	// Report zero dropped counts instead of panicking on a nil receiver.
	var totalDropped uint64
	if s.eng.processing.dataBus != nil {
		totalDropped = uint64(s.eng.processing.dataBus.Dropped() + s.eng.processing.dataBus.PushDropped() + s.eng.processing.dataBus.HighPriorityPushDropped() + log.Dropped())
	}

	return core.EngineStats{
		Status:         s.eng.runtime.status,
		Uptime:         uptime,
		Drivers:        len(s.eng.driverReg.drivers),
		Transports:     len(s.eng.transportMgr.transports),
		Rules:          len(s.eng.ruleSet.ruleEngine.Rules()),
		TotalRead:      totalRead,
		TotalPublish:   s.totalPublish.Load(),
		TotalErrors:    s.totalErrors.Load(),
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
// The offline buffer is a single shared instance handed to every batcher
// in newTransportBatcher, so pending and drained are read once from it;
// pushed is a genuine per-batcher counter (offlineBufferPushes,
// incremented in publishWithRetryAndBuffer) and is summed across batchers.
//
// The batchers map is snapshotted under RLock and then released before
// reading the counters, mirroring the Stop() pattern, so counter reads
// never block management API calls that take the write lock.
func (s *statsCollector) OfflineBufferStats() (pending int, drained, pushed uint64) {
	s.eng.mu.RLock()
	batchers := make([]*transportBatcher, 0, len(s.eng.transportMgr.batchers))
	for _, b := range s.eng.transportMgr.batchers {
		batchers = append(batchers, b)
	}
	buf := s.eng.transportMgr.offlineBuffer
	s.eng.mu.RUnlock()

	// pushed is per-batcher: aggregate it.
	for _, b := range batchers {
		pushed += b.retry.offlineBufferPushes.Load()
	}

	// pending and drained are properties of the shared buffer; read them
	// once to avoid double-counting across batchers.
	if buf != nil {
		pending = buf.PendingCount()
		drained = buf.DrainCount()
	}
	return pending, drained, pushed
}

// OfflineBufferBytes returns the total on-disk bytes held by the shared
// offline buffer (gauge corec_offline_buffer_bytes).
func (s *statsCollector) OfflineBufferBytes() int64 {
	s.eng.mu.RLock()
	buf := s.eng.transportMgr.offlineBuffer
	s.eng.mu.RUnlock()
	if buf == nil {
		return 0
	}
	return buf.PendingBytes()
}

// OfflineBufferEvictions returns the total number of batches evicted
// from the offline buffer because it was full (oldest dropped). The
// buffer is a single shared instance (see OfflineBufferStats), so the
// count is read once. Backs the corec_offline_buffer_evicted_total
// Prometheus counter via the hub/route optional-role assertion.
func (s *statsCollector) OfflineBufferEvictions() uint64 {
	s.eng.mu.RLock()
	buf := s.eng.transportMgr.offlineBuffer
	s.eng.mu.RUnlock()
	if buf == nil {
		return 0
	}
	return buf.EvictedCount()
}

// FlushBatchesDropped returns the total number of full batches that were
// evicted or dropped from batcher flush queues because the async flush
// consumer fell behind a sustained burst (async-batcher backpressure
// observability). It aggregates the per-batcher
// flushBatchesDropped counters. Intended for Prometheus exposure as
// corec_flush_batches_dropped_total.
func (s *statsCollector) FlushBatchesDropped() uint64 {
	s.eng.mu.RLock()
	batchers := make([]*transportBatcher, 0, len(s.eng.transportMgr.batchers))
	for _, b := range s.eng.transportMgr.batchers {
		batchers = append(batchers, b)
	}
	s.eng.mu.RUnlock()

	var total uint64
	for _, b := range batchers {
		total += b.buf.flushBatchesDropped.Load()
	}
	return total
}

// DriverMetrics exposes the aggregated driver-metrics registry for the
// route layer (core.DriverMetricsProvider).
func (s *statsCollector) DriverMetrics() []core.DriverMetric {
	snap := s.driverMetrics.Snapshot()
	out := make([]core.DriverMetric, 0, len(snap))
	for _, m := range snap {
		out = append(out, core.DriverMetric{
			Name: m.Name, Kind: string(m.Kind), Labels: m.Labels, Value: m.Value,
		})
	}
	return out
}

func (s *statsCollector) recordBadQualityDrop() {
	s.badQualityDropped.Add(1)
}

// DroppedBreakdown decomposes the drop counters by cause
// (core.DroppedBreakdownProvider).
func (s *statsCollector) DroppedBreakdown() core.DroppedBreakdown {
	bd := core.DroppedBreakdown{
		BadQuality: s.badQualityDropped.Load(),
		LogEvents:  uint64(log.Dropped()),
	}
	if s.eng.processing.dataBus != nil {
		bd.DataBusMain = uint64(s.eng.processing.dataBus.PushDropped())
		bd.DataBusPriority = uint64(s.eng.processing.dataBus.HighPriorityPushDropped())
		bd.SlowSubscriber = uint64(s.eng.processing.dataBus.Dropped())
	}
	return bd
}

// recordRead counts n points entering the pipeline (driver reads and
// chained-transport inbound points alike): the lifetime total and the
// windowed per-second rate behind EngineStats.PointsPerSec.
func (s *statsCollector) recordRead(n int) {
	if n <= 0 {
		return
	}
	s.totalRead.Add(uint64(n))
	s.readRate.Observe(time.Now(), uint64(n))
}

// onBatchPublish is the callback handed to each transportBatcher via
// newTransportBatcher. It is called with the number of points actually
// published when publishWithRetryAndBuffer or drainOnce succeeds — not
// when points are merely enqueued for async flush. This keeps totalPublish
// accurate for batched transports, where the real PublishBatch happens
// asynchronously after publish() returns.
func (s *statsCollector) onBatchPublish(n int) {
	s.totalPublish.Add(uint64(n))
}
