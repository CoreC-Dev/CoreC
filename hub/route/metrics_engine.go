package route

import (
	"fmt"
	"sort"
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

	// Capacity-based eviction observability (stage-B audit finding B2.1):
	// a full buffer drops its oldest batches (newest data wins), and
	// those drops must be visible to operators, not just logged. The
	// engine optionally provides the eviction count; the role is
	// asserted locally rather than added to core because it is a single
	// consumer's concern.
	// Byte-level water mark (roadmap #28): batch count alone cannot
	// answer "how much disk is the outage consuming?"
	if bp, ok := eng.(interface {
		OfflineBufferBytes() int64
	}); ok && bp != nil {
		writePromHeader(b, "corec_offline_buffer_bytes", "gauge", "Total on-disk bytes held by the offline buffer awaiting replay")
		fmt.Fprintf(b, "corec_offline_buffer_bytes %d\n", bp.OfflineBufferBytes())
	}

	if ep, ok := eng.(interface {
		OfflineBufferEvictions() uint64
	}); ok && ep != nil {
		writePromHeader(b, "corec_offline_buffer_evicted_total", "counter", "Total batches evicted from the offline buffer when full (oldest dropped, newest wins)")
		fmt.Fprintf(b, "corec_offline_buffer_evicted_total %d\n", ep.OfflineBufferEvictions())
	}
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

// writeDeadLetterMetrics exposes the write dead-letter queue for
// alerting (audit finding OBS-3 / roadmap #5): the queue is capped and
// in-memory, so "commands are piling up" was previously invisible to
// Prometheus — operators had to poll GET /write/failed. Engines that do
// not satisfy core.DeadLetterStatsProvider (test mocks) omit the
// families.
func writeDeadLetterMetrics(b *strings.Builder) {
	provider, ok := getEngine().(core.DeadLetterStatsProvider)
	if !ok || provider == nil {
		return
	}
	size, pushed := provider.DeadLetterStats()

	writePromHeader(b, "corec_dead_letter_queue_size", "gauge", "Write commands currently held in the dead letter queue (retries exhausted); alert on sustained growth")
	fmt.Fprintf(b, "corec_dead_letter_queue_size %d\n", size)

	writePromHeader(b, "corec_dead_letter_pushed_total", "counter", "Total write commands pushed into the dead letter queue since startup")
	fmt.Fprintf(b, "corec_dead_letter_pushed_total %d\n", pushed)
}

// writeDroppedBreakdown emits the per-cause drop counters so operators
// can attribute loss instead of diffing the umbrella total (audit
// finding OBS-6: corec_dropped_total historically summed four unrelated
// kinds of loss — data-bus overflow, slow subscribers, bad-quality
// policy discards, and dropped log events — with no labels). The
// umbrella family stays for backward compatibility; engines without
// the core.DroppedBreakdownProvider role omit the breakdown.
func writeDroppedBreakdown(b *strings.Builder) {
	provider, ok := getEngine().(core.DroppedBreakdownProvider)
	if !ok || provider == nil {
		return
	}
	bd := provider.DroppedBreakdown()

	writePromHeader(b, "corec_databus_dropped_total", "counter", "Data points dropped from the internal data bus because it was full, by channel (oldest evicted per push)")
	fmt.Fprintf(b, "corec_databus_dropped_total{channel=\"main\"} %d\n", bd.DataBusMain)
	fmt.Fprintf(b, "corec_databus_dropped_total{channel=\"priority\"} %d\n", bd.DataBusPriority)

	writePromHeader(b, "corec_slow_subscriber_dropped_total", "counter", "Data points dropped because a /tags/stream subscriber fell behind (bounded per-subscriber buffers)")
	fmt.Fprintf(b, "corec_slow_subscriber_dropped_total %d\n", bd.SlowSubscriber)

	writePromHeader(b, "corec_bad_quality_dropped_total", "counter", "Data points discarded by the on-bad-quality: drop policy (a configured policy, not a processing error)")
	fmt.Fprintf(b, "corec_bad_quality_dropped_total %d\n", bd.BadQuality)
}

// writeDriverEmittedMetrics emits the driver-emitted protocol-level series
// aggregated by the engine's registry (audit finding OBS-8: the
// core.Metrics port existed but was only ever wired to NoopMetrics —
// driver emissions went nowhere). Series render verbatim under their
// driver-owned names (modbus_*, opcua_*, s7_*); HELP names the driver
// namespace explicitly. Engines without the role omit the families.
func writeDriverEmittedMetrics(b *strings.Builder) {
	provider, ok := getEngine().(core.DriverMetricsProvider)
	if !ok || provider == nil {
		return
	}
	series := provider.DriverMetrics()
	if len(series) == 0 {
		return
	}

	// Emit one HELP/TYPE pair per metric name, then its series.
	family := ""
	for _, m := range series {
		if m.Name != family {
			family = m.Name
			writePromHeader(b, m.Name, m.Kind, "Driver-emitted protocol metric (see docs/config/drivers.md)")
		}
		// Labels render in sorted key order so exposition is
		// deterministic (Prometheus accepts any label order; stable
		// text keeps test snapshots and scraping diffs meaningful).
		labelKeys := make([]string, 0, len(m.Labels))
		for k := range m.Labels {
			labelKeys = append(labelKeys, k)
		}
		sort.Strings(labelKeys)
		labelStr := ""
		for _, k := range labelKeys {
			labelStr += fmt.Sprintf("%s=%q,", k, promEscape(m.Labels[k]))
		}
		fmt.Fprintf(b, "%s{%s} %g\n", m.Name, strings.TrimSuffix(labelStr, ","), m.Value)
	}
}

// writeAlertMetrics exposes the alert registry for alerting on alerts
// (audit findings OBS-4/HUB-6): the cumulative fire count per rule and
// the number of currently-active alerts. Engines without the
// core.AlertMetricsProvider role (test mocks) omit the families.
func writeAlertMetrics(b *strings.Builder) {
	provider, ok := getEngine().(core.AlertMetricsProvider)
	if !ok || provider == nil {
		return
	}

	fires := provider.AlertFireCounts()
	if len(fires) > 0 {
		rules := make([]string, 0, len(fires))
		for rule := range fires {
			rules = append(rules, rule)
		}
		sort.Strings(rules)
		writePromHeader(b, "corec_alerts_total", "counter", "Total rule alert fires per rule (raw, not deduplicated); rate() shows firing frequency")
		for _, rule := range rules {
			fmt.Fprintf(b, "corec_alerts_total{rule=%q} %d\n", promEscape(rule), fires[rule])
		}
	}

	writePromHeader(b, "corec_alerts_active", "gauge", "Currently-active alerts (fired within the active window, acknowledged or not)")
	fmt.Fprintf(b, "corec_alerts_active %d\n", provider.ActiveAlertCount())
}

// writeLatencyMetrics emits the read and publish latency histogram
// families plus the data-age (freshness) family.
//
// When the engine satisfies core.LabeledLatencyProvider (audit finding
// OBS-2), the families carry driver/transport labels —
// corec_read_latency_seconds_bucket{driver="plc1",le="..."} — so a
// single slow driver or transport is distinguishable from healthy
// siblings; aggregate queries use sum by (le) (...) in PromQL. Engines
// without the role (test mocks) fall back to the unlabeled families.
func writeLatencyMetrics(b *strings.Builder) {
	eng := getEngine()

	if labeled, ok := eng.(core.LabeledLatencyProvider); ok && labeled != nil {
		writeLabeledHistogram(b, "corec_read_latency_seconds", "Driver read latency in seconds, per driver", "driver", labeled.ReadLatencyByDriver())
		writeLabeledHistogram(b, "corec_publish_latency_seconds", "Transport publish latency in seconds, per transport", "transport", labeled.PublishLatencyByTransport())
		writeLabeledHistogram(b, "corec_data_age_seconds", "Data age (publish time minus collection timestamp) in seconds, per transport", "transport", labeled.DataAgeByTransport())
		return
	}

	// Unlabeled fallback: aggregate histograms only.
	if provider, ok := eng.(core.LatencyProvider); ok && provider != nil {
		writeLatencyHistogram(b, "corec_read_latency_seconds", "Driver read latency in seconds", nil, provider.ReadLatencyHistogram())
		writeLatencyHistogram(b, "corec_publish_latency_seconds", "Transport publish latency in seconds", nil, provider.PublishLatencyHistogram())
	}
	if ap, ok := eng.(core.DataAgeProvider); ok && ap != nil {
		writeLatencyHistogram(b, "corec_data_age_seconds", "Data age (publish time minus collection timestamp) in seconds", nil, ap.DataAgeHistogram())
	}
}

// writeLabeledHistogram renders one histogram family as a series per
// label value, in sorted label order for deterministic output. An
// empty snapshot map emits nothing (no family header with zero
// samples), matching the unlabeled path's behaviour for empty data.
func writeLabeledHistogram(b *strings.Builder, name, help, label string, snaps map[string]core.LatencySnapshot) {
	if len(snaps) == 0 {
		return
	}
	keys := make([]string, 0, len(snaps))
	for k := range snaps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeLatencyHistogram(b, name, help, map[string]string{label: key}, snaps[key])
	}
}

// writeLatencyHistogram renders a single Prometheus histogram series
// (one label set) from a LatencySnapshot. Buckets and Counts are
// already cumulative (the histogram implementation increments every
// bucket whose upper bound >= the observed value), so they are emitted
// directly. A nil labels map renders the unlabeled series.
func writeLatencyHistogram(b *strings.Builder, name, help string, labels map[string]string, snap core.LatencySnapshot) {
	writePromHeader(b, name, "histogram", help)
	// labelPrefix carries the caller's labels each followed by a comma
	// (e.g. `driver="plc1",`), so bucket lines splice `le=` in after
	// them. sum/count lines use the comma-trimmed form, and render
	// brace-less when there are no labels (identical to the
	// pre-labeled exposition format).
	labelPrefix := ""
	for k, v := range labels {
		labelPrefix += fmt.Sprintf("%s=%q,", k, promEscape(v))
	}
	suffix := strings.TrimSuffix(labelPrefix, ",")
	for i, ub := range snap.Buckets {
		var le string
		if i < len(snap.Counts) {
			le = strconv.FormatFloat(ub, 'f', -1, 64)
			fmt.Fprintf(b, "%s_bucket{%sle=%q} %d\n", name, labelPrefix, le, snap.Counts[i])
		}
	}
	// +Inf bucket = total count
	fmt.Fprintf(b, "%s_bucket{%sle=\"+Inf\"} %d\n", name, labelPrefix, snap.Count)
	if suffix != "" {
		fmt.Fprintf(b, "%s_sum{%s} %g\n", name, suffix, snap.Sum)
		fmt.Fprintf(b, "%s_count{%s} %d\n", name, suffix, snap.Count)
	} else {
		fmt.Fprintf(b, "%s_sum %g\n", name, snap.Sum)
		fmt.Fprintf(b, "%s_count %d\n", name, snap.Count)
	}
}
