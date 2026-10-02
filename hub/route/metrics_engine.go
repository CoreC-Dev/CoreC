package route

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
)

// writeOfflineBufferMetrics emits offline-buffer depth and counters.
// The engine optionally satisfies core.OfflineBufferStatsProvider; when it
// does not (e.g. test mocks), no metrics are emitted.
func writeOfflineBufferMetrics(b *strings.Builder) {
	eng := getEngine()
	provider, ok := eng.(core.OfflineBufferStatsProvider)
	if !ok || provider == nil {
		return
	}
	pending, drained, pushed := provider.OfflineBufferStats()

	writePromHeader(b, "corec_offline_buffer_pending", "gauge", "Batches currently held in the offline buffer awaiting replay")
	fmt.Fprintf(b, "corec_offline_buffer_pending %d\n", pending)

	writePromHeader(b, "corec_offline_buffer_drained_total", "counter", "Total batches successfully replayed from the offline buffer")
	fmt.Fprintf(b, "corec_offline_buffer_drained_total %d\n", drained)

	writePromHeader(b, "corec_offline_buffer_pushed_total", "counter", "Total batches persisted to the offline buffer after retries exhausted")
	fmt.Fprintf(b, "corec_offline_buffer_pushed_total %d\n", pushed)
}

// writeFlushBatchesDropped emits the total number of full batches evicted
// or dropped from batcher flush queues because the async flush consumer
// fell behind a sustained burst (IMPROVEMENTS #1: async batcher
// backpressure observability).
func writeFlushBatchesDropped(b *strings.Builder) {
	eng := getEngine()
	provider, ok := eng.(core.FlushBatchesDroppedProvider)
	if !ok || provider == nil {
		return
	}
	dropped := provider.FlushBatchesDropped()
	writePromHeader(b, "corec_flush_batches_dropped_total", "counter", "Full batches evicted or dropped from batcher flush queues due to sustained backpressure")
	fmt.Fprintf(b, "corec_flush_batches_dropped_total %d\n", dropped)
}

// writeLatencyMetrics emits read and publish latency histograms from the
// engine's LatencyProvider role. When the engine does not satisfy
// core.LatencyProvider (e.g. test mocks), no metrics are emitted.
func writeLatencyMetrics(b *strings.Builder) {
	eng := getEngine()

	// Latency histograms (read/publish) are exposed via the LatencyProvider
	// role interface. When the engine does not satisfy it (e.g. test
	// mocks), no latency metrics are emitted.
	if provider, ok := eng.(core.LatencyProvider); ok && provider != nil {
		writeLatencyHistogram(b, "corec_read_latency_seconds", "Driver read latency in seconds", provider.ReadLatencyHistogram())
		writeLatencyHistogram(b, "corec_publish_latency_seconds", "Transport publish latency in seconds", provider.PublishLatencyHistogram())
	}

	// Data-age (freshness) histogram is exposed via a separate role
	// interface so it is emitted independently of LatencyProvider.
	if ap, ok := eng.(core.DataAgeProvider); ok && ap != nil {
		writeLatencyHistogram(b, "corec_data_age_seconds", "Data age (publish time minus collection timestamp) in seconds", ap.DataAgeHistogram())
	}
}

// writeLatencyHistogram renders a single Prometheus histogram family from
// a LatencySnapshot. Buckets and Counts are already cumulative (the
// histogram implementation increments every bucket whose upper bound >=
// the observed value), so we emit them directly.
func writeLatencyHistogram(b *strings.Builder, name, help string, snap core.LatencySnapshot) {
	writePromHeader(b, name, "histogram", help)
	for i, ub := range snap.Buckets {
		var le string
		if i < len(snap.Counts) {
			le = strconv.FormatFloat(ub, 'f', -1, 64)
			fmt.Fprintf(b, "%s_bucket{le=%q} %d\n", name, le, snap.Counts[i])
		}
	}
	// +Inf bucket = total count
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", name, snap.Count)
	fmt.Fprintf(b, "%s_sum %g\n", name, snap.Sum)
	fmt.Fprintf(b, "%s_count %d\n", name, snap.Count)
}
