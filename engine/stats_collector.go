// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/metrics"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine/statistic"
	"github.com/CoreC-Dev/CoreC/log"
)

// statsCollector groups the engine's observability state: throughput
// counters (reads/publishes/errors), the uptime anchor, the read/publish/
// data-age latency histograms, and the per-engine statistic manager.
// Extracted from CoreCEngine in the CPLX-020 God Object split;
// CoreCEngine keeps thin forwarders so the core.StatsProvider,
// core.LatencyProvider, core.DataAgeProvider, and the optional
// offline-buffer/flush-drop provider method sets are unchanged.
type statsCollector struct {
	totalRead    atomic.Uint64
	totalPublish atomic.Uint64
	totalErrors  atomic.Uint64
	startTime    time.Time

	// readRate tracks points-per-second over the last minute so
	// EngineStats.PointsPerSec reports a true current rate. The
	// lifetime average previously reported (totalRead / uptime) decayed
	// slowly on a stalled pipeline and was useless for "collection
	// stopped" alerting (audit finding OBS-1).
	readRate *metrics.RateWindow

	// labeled owns every latency histogram in the engine: the global
	// read/publish/data-age aggregates AND the per-driver /
	// per-transport labeled histograms (audit finding OBS-2: one slow
	// driver was invisible behind healthy siblings in the single
	// aggregate). A dedicated sub-role keeps this collector under its
	// CPLX field/method ceilings.
	labeled *labeledLatencyStore

	// statManager collects throughput counters (reads/publishes/errors).
	// It is an instance field rather than the package-level
	// statistic.DefaultManager so that multiple Engine instances embedded
	// in the same process do not mix their counters (IMPROVEMENTS #8).
	// Injected into the scheduler via NewScheduler.
	statManager *statistic.Manager

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

	// Current rate over the last minute (a true windowed rate, not the
	// lifetime average). For an engine younger than the window, fall
	// back to the uptime as the divisor so the first seconds are not
	// diluted by a window the engine was not alive for (audit finding
	// OBS-1: the old lifetime average never showed a stall).
	pointsPerSec := float64(0)
	windowSecs := s.readRate.WindowSeconds()
	elapsedSecs := int64(uptime.Seconds())
	divisor := windowSecs
	if elapsedSecs < divisor {
		divisor = elapsedSecs
	}
	// A brand-new engine (sub-second uptime) still reports its first
	// second's traffic at face value instead of 0; the floor disappears
	// once uptime exceeds one second.
	if divisor < 1 {
		divisor = 1
	}
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
// in newTransportBatcher, so pending and drained are read once from it
// rather than summed per batcher — summing the per-batcher
// OfflineBufferPending/OfflineBufferDrained values would multiply the real
// count by the number of batchers, since they all delegate to the same
// buffer. pushed, by contrast, is a genuine per-batcher counter
// (offlineBufferPushes, incremented in publishWithRetryAndBuffer) and must
// be summed across batchers.
//
// The batchers map is snapshotted under RLock and then released before
// reading the counters, mirroring the Stop() pattern, so a slow Len() (disk
// readdir) can never block management API calls that take the write lock.
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

// OfflineBufferEvictions returns the total number of batches evicted
// from the offline buffer because it was full (oldest dropped, newest
// data wins). The buffer is a single shared instance (see
// OfflineBufferStats), so the count is read once. Backs the
// corec_offline_buffer_evicted_total Prometheus counter via the
// hub/route optional-role assertion (stage-B audit finding B2.1:
// capacity-based evictions drop real data and must be observable, not
// just logged).
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
// consumer fell behind a sustained burst (IMPROVEMENTS #1: async batcher
// backpressure observability). It aggregates the per-batcher
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
// published when publishWithRetryAndBuffer or drainOnce succeeds — not when
// points are merely enqueued for async flush. This keeps totalPublish and
// the statistic manager's publish counter accurate for batched transports,
// where the real PublishBatch happens asynchronously after publish()
// returns (Finding 4 fix: previously publish.go counted totalPublish and
// statManager.PushPublish immediately after the near-instant enqueue, which
// inflated the publish counter and undercounted errors when batches later
// failed after retries).
func (s *statsCollector) onBatchPublish(n int) {
	s.totalPublish.Add(uint64(n))
	s.statManager.PushPublish(int64(n))
}
