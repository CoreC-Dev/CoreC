package engine

import (
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/common/metrics"
	"github.com/CoreC-Dev/CoreC/core"
)

// latencyObserver receives one latency or data-age observation. Both
// the plain metrics.LatencyHistogram and the engine's fan-out adapters
// (global + per-driver/transport labeled histogram, audit OBS-2)
// satisfy it, so the batcher accepts either wiring without knowing the
// difference.
type latencyObserver interface {
	Observe(d time.Duration)
}

// transportLatencyFanout forwards each observation to the global
// aggregate histogram AND the per-transport labeled histogram, so the
// unlabeled series keep their meaning while per-transport series become
// available (audit finding OBS-2: the single global histogram let one
// slow transport hide behind healthy siblings).
type transportLatencyFanout struct {
	global  *metrics.LatencyHistogram
	labeled *metrics.LatencyHistogram
}

func (f transportLatencyFanout) Observe(d time.Duration) {
	f.global.Observe(d)
	f.labeled.Observe(d)
}

// labeledLatencyStore owns every latency histogram in the engine: the
// three global aggregates (read/publish/data-age) and the labeled
// per-driver / per-transport histograms exposed via
// core.LabeledLatencyProvider (audit finding OBS-2).
//
// It is a dedicated sub-role of statsCollector (CPLX discipline: the
// collector stays under its field/method ceilings). Entries are created
// lazily on first observation; drivers/transports removed at runtime
// leave a frozen (monotonic) series behind — standard Prometheus-client
// behaviour — rather than erasing history.
type labeledLatencyStore struct {
	globalRead    *metrics.LatencyHistogram
	globalPublish *metrics.LatencyHistogram
	globalAge     *metrics.LatencyHistogram

	mu             sync.RWMutex
	readByDriver   map[string]*metrics.LatencyHistogram
	pubByTransport map[string]*metrics.LatencyHistogram
	ageByTransport map[string]*metrics.LatencyHistogram
}

// newLabeledLatencyStore creates the store with the three global
// aggregate histograms on their default bucket scales.
func newLabeledLatencyStore() *labeledLatencyStore {
	return &labeledLatencyStore{
		globalRead:    metrics.NewLatencyHistogram(metrics.DefaultReadLatencyBuckets),
		globalPublish: metrics.NewLatencyHistogram(metrics.DefaultPublishLatencyBuckets),
		globalAge:     metrics.NewLatencyHistogram(metrics.DefaultDataAgeBuckets),
		readByDriver:  make(map[string]*metrics.LatencyHistogram),
		pubByTransport: make(map[string]*metrics.LatencyHistogram),
		ageByTransport: make(map[string]*metrics.LatencyHistogram),
	}
}

// histogramFor returns the lazily-created labeled histogram for key in
// the given map, creating it with the bucket scale on first use.
func histogramFor(m map[string]*metrics.LatencyHistogram, mu *sync.RWMutex, key string, buckets []float64) *metrics.LatencyHistogram {
	mu.RLock()
	h, ok := m[key]
	mu.RUnlock()
	if ok {
		return h
	}
	mu.Lock()
	defer mu.Unlock()
	if h, ok = m[key]; ok {
		return h
	}
	h = metrics.NewLatencyHistogram(buckets)
	m[key] = h
	return h
}

// observeRead records a driver read latency in the global aggregate and
// the per-driver labeled histogram.
func (l *labeledLatencyStore) observeRead(driver string, d time.Duration) {
	l.globalRead.Observe(d)
	histogramFor(l.readByDriver, &l.mu, driver, metrics.DefaultReadLatencyBuckets).Observe(d)
}

// observePublish records a transport publish latency in the global
// aggregate and the per-transport labeled histogram. Used by the
// direct (non-batched) publish paths, which know their target name.
func (l *labeledLatencyStore) observePublish(transport string, d time.Duration) {
	l.globalPublish.Observe(d)
	histogramFor(l.pubByTransport, &l.mu, transport, metrics.DefaultPublishLatencyBuckets).Observe(d)
}

// observeAge records a data age in the global aggregate and the
// per-transport labeled histogram.
func (l *labeledLatencyStore) observeAge(transport string, d time.Duration) {
	l.globalAge.Observe(d)
	histogramFor(l.ageByTransport, &l.mu, transport, metrics.DefaultDataAgeBuckets).Observe(d)
}

// publishObserver returns the observer the named transport's batcher
// records publish latency into: the global aggregate plus the
// per-transport labeled histogram (fan-out).
func (l *labeledLatencyStore) publishObserver(transport string) latencyObserver {
	return transportLatencyFanout{
		global:  l.globalPublish,
		labeled: histogramFor(l.pubByTransport, &l.mu, transport, metrics.DefaultPublishLatencyBuckets),
	}
}

// ageObserver returns the observer the named transport's batcher
// records data age into: the global aggregate plus the per-transport
// labeled histogram (fan-out).
func (l *labeledLatencyStore) ageObserver(transport string) latencyObserver {
	return transportLatencyFanout{
		global:  l.globalAge,
		labeled: histogramFor(l.ageByTransport, &l.mu, transport, metrics.DefaultDataAgeBuckets),
	}
}

// snapshotOf copies one histogram into the wire snapshot type.
func snapshotOf(h *metrics.LatencyHistogram) core.LatencySnapshot {
	buckets, counts, sum, count := h.Snapshot()
	return core.LatencySnapshot{Buckets: buckets, Counts: counts, Sum: sum, Count: count}
}

// globalReadSnapshot returns the aggregate read-latency snapshot
// (core.LatencyProvider role).
func (l *labeledLatencyStore) globalReadSnapshot() core.LatencySnapshot {
	return snapshotOf(l.globalRead)
}

// globalPublishSnapshot returns the aggregate publish-latency snapshot
// (core.LatencyProvider role).
func (l *labeledLatencyStore) globalPublishSnapshot() core.LatencySnapshot {
	return snapshotOf(l.globalPublish)
}

// globalAgeSnapshot returns the aggregate data-age snapshot
// (core.DataAgeProvider role).
func (l *labeledLatencyStore) globalAgeSnapshot() core.LatencySnapshot {
	return snapshotOf(l.globalAge)
}

// snapshotsOf converts a labeled histogram map into per-key snapshots.
func snapshotsOf(m map[string]*metrics.LatencyHistogram) map[string]core.LatencySnapshot {
	out := make(map[string]core.LatencySnapshot, len(m))
	for k, h := range m {
		out[k] = snapshotOf(h)
	}
	return out
}

// readSnapshots returns per-driver read-latency snapshots
// (core.LabeledLatencyProvider).
func (l *labeledLatencyStore) readSnapshots() map[string]core.LatencySnapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return snapshotsOf(l.readByDriver)
}

// publishSnapshots returns per-transport publish-latency snapshots
// (core.LabeledLatencyProvider).
func (l *labeledLatencyStore) publishSnapshots() map[string]core.LatencySnapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return snapshotsOf(l.pubByTransport)
}

// ageSnapshots returns per-transport data-age snapshots
// (core.LabeledLatencyProvider).
func (l *labeledLatencyStore) ageSnapshots() map[string]core.LatencySnapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return snapshotsOf(l.ageByTransport)
}
