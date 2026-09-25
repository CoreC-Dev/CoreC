// Package metrics provides lightweight metric collection primitives.
//
// It contains self-contained, dependency-free metric types (currently
// LatencyHistogram) that the engine uses to record operation latencies.
// The values are exposed to scrapers by the route layer's Prometheus
// text-format handler; no third-party Prometheus client library is
// pulled in, keeping the core industrial gateway dependency-light.
package metrics

import (
	"sync"
	"time"
)

// DefaultReadLatencyBuckets are the histogram bucket upper bounds (in
// seconds) used for driver read latency. The scale covers sub-millisecond
// field-bus reads through multi-second reads typical of PLC/OPC-UA I/O
// over slow or congested links.
var DefaultReadLatencyBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

// DefaultPublishLatencyBuckets are the histogram bucket upper bounds (in
// seconds) used for transport publish latency. It uses the same scale as
// read latency since both are short-lived network I/O operations.
var DefaultPublishLatencyBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

// DefaultDataAgeBuckets are the histogram bucket upper bounds (in seconds)
// used for data age (freshness): the elapsed time between a data point's
// collection timestamp (DataPoint.Timestamp) and the moment the engine
// attempts to publish it. The scale is larger than I/O latency buckets
// because data age includes scheduler queuing, batching, and — during
// transport outages — offline-buffer replay, so values can reach minutes.
var DefaultDataAgeBuckets = []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300, 600}

// LatencyHistogram is a lightweight, mutex-guarded histogram for tracking
// operation latencies without a full metrics client dependency.
//
// It follows the Prometheus cumulative-bucket convention: each entry in
// counts is the number of observations whose duration is less than or
// equal to the corresponding entry in buckets (i.e. counts are
// cumulative). An implicit +Inf bucket equal to count is assumed when
// rendering; observations that exceed every configured bucket increment
// only count and sum, not any counts entry.
//
// All methods are safe for concurrent use.
type LatencyHistogram struct {
	mu      sync.Mutex
	buckets []float64 // upper bounds in seconds, expected sorted ascending
	counts  []uint64  // cumulative bucket counts, aligned with buckets
	sum     float64   // total observed duration in seconds
	count   uint64    // total number of observations
}

// NewLatencyHistogram creates a LatencyHistogram with the given bucket
// upper bounds (in seconds). The buckets slice is copied so later
// mutation of the caller's slice does not affect the histogram. Callers
// should pass a strictly increasing, ascending-sorted slice (e.g.
// DefaultReadLatencyBuckets); a nil or empty slice yields a histogram
// that records only the sum and count.
func NewLatencyHistogram(buckets []float64) *LatencyHistogram {
	b := make([]float64, len(buckets))
	copy(b, buckets)
	return &LatencyHistogram{
		buckets: b,
		counts:  make([]uint64, len(b)),
	}
}

// Observe records a single observation of duration d. It increments
// every cumulative bucket whose upper bound is greater than or equal to
// d (maintaining the Prometheus cumulative invariant), adds d to the
// running sum, and increments the total observation count. Observations
// that exceed all configured buckets still update sum and count.
func (h *LatencyHistogram) Observe(d time.Duration) {
	secs := d.Seconds()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += secs
	h.count++
	for i, ub := range h.buckets {
		if secs <= ub {
			h.counts[i]++
		}
	}
}

// Snapshot returns a thread-safe copy of the histogram's current state.
// The returned slices are independent copies and may be mutated freely
// by the caller. buckets is the list of upper bounds (in seconds),
// counts is the aligned list of cumulative per-bucket counts, sum is
// the total observed duration in seconds, and count is the total number
// of observations.
func (h *LatencyHistogram) Snapshot() (buckets []float64, counts []uint64, sum float64, count uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := make([]float64, len(h.buckets))
	copy(b, h.buckets)
	c := make([]uint64, len(h.counts))
	copy(c, h.counts)
	return b, c, h.sum, h.count
}
