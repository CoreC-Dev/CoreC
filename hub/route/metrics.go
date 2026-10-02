package route

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
)

// promContentType is the Content-Type for the Prometheus text exposition
// format (version 0.0.4). Scrapers use it to select the right parser.
const promContentType = "text/plain; version=0.0.4; charset=utf-8"

// promMetrics exposes engine statistics in Prometheus text exposition format.
// It is registered inside the authenticated route group, so a scraper must
// present the API secret just like any other protected endpoint. The metrics
// are produced entirely from the engine's existing Stats() method — no
// external Prometheus client library is used. promMetrics only depends on
// the StatsProvider role of the engine.
func promMetrics(w http.ResponseWriter, r *http.Request) {
	var sp core.StatsProvider = getEngine()
	stats := sp.Stats()

	var b strings.Builder

	// ─── Counters (monotonically increasing totals) ───────────────────
	writePromHeader(&b, "corec_reads_total", "counter", "Total data points read from drivers")
	fmt.Fprintf(&b, "corec_reads_total %d\n", stats.TotalRead)

	writePromHeader(&b, "corec_publishes_total", "counter", "Total data points published to transports")
	fmt.Fprintf(&b, "corec_publishes_total %d\n", stats.TotalPublish)

	writePromHeader(&b, "corec_errors_total", "counter", "Total processing errors")
	fmt.Fprintf(&b, "corec_errors_total %d\n", stats.TotalErrors)

	writePromHeader(&b, "corec_dropped_total", "counter", "Total data points dropped")
	fmt.Fprintf(&b, "corec_dropped_total %d\n", stats.TotalDropped)

	writePromHeader(&b, "corec_log_dropped_total", "counter", "Total log events dropped due to slow consumers (full source channel or subscriber buffer)")
	fmt.Fprintf(&b, "corec_log_dropped_total %d\n", log.Dropped())

	// ─── Gauges (instantaneous values) ────────────────────────────────
	// Note: gauges must NOT use the _total suffix — that is reserved for
	// counters (monotonically increasing values) per Prometheus/OpenMetrics
	// convention.
	writePromHeader(&b, "corec_drivers", "gauge", "Number of configured drivers")
	fmt.Fprintf(&b, "corec_drivers %d\n", stats.Drivers)

	writePromHeader(&b, "corec_transports", "gauge", "Number of configured transports")
	fmt.Fprintf(&b, "corec_transports %d\n", stats.Transports)

	writePromHeader(&b, "corec_rules", "gauge", "Number of configured rules")
	fmt.Fprintf(&b, "corec_rules %d\n", stats.Rules)

	writePromHeader(&b, "corec_uptime_seconds", "gauge", "Engine uptime in seconds")
	fmt.Fprintf(&b, "corec_uptime_seconds %g\n", stats.Uptime.Seconds())

	writePromHeader(&b, "corec_points_per_second", "gauge", "Current data points processed per second")
	fmt.Fprintf(&b, "corec_points_per_second %g\n", stats.PointsPerSec)

	// ─── Per-driver metrics ───────────────────────────────────────────
	writeDriverMetrics(&b, stats.DriverStats)

	// ─── Per-transport metrics ────────────────────────────────────────
	writeTransportMetrics(&b, stats.TransportStats)

	// ─── Offline buffer metrics ───────────────────────────────────────
	writeOfflineBufferMetrics(&b)

	// ─── Batcher flush-queue drop counter ──────────────────────────────
	writeFlushBatchesDropped(&b)

	// ─── HTTP request metrics ──────────────────────────────────────────
	writeHTTPMetrics(&b)

	// ─── Latency histograms ───────────────────────────────────────────
	writeLatencyMetrics(&b)

	// ─── Go runtime / process metrics ─────────────────────────────────
	writeRuntimeMetrics(&b)

	w.Header().Set("Content-Type", promContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// writePromHeader writes the HELP and TYPE preamble lines for a metric family.
func writePromHeader(b *strings.Builder, name, typ, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

// promEscape escapes a label value for the Prometheus text format. The
// backslash, double-quote, and newline characters must be escaped; everything
// else (including UTF-8) is emitted verbatim.
func promEscape(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// sortedKeys returns the keys of a map[string]V in sorted order. It is
// generic so it works for both DriverStatus and TransportStatus maps.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
