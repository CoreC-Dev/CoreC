package route

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
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

	// ─── Go runtime / process metrics ─────────────────────────────────
	writeRuntimeMetrics(&b)

	w.Header().Set("Content-Type", promContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// writeDriverMetrics emits per-driver counter and gauge families. Drivers are
// iterated in sorted name order so the output is deterministic, which keeps
// test snapshots stable and aids diffing.
func writeDriverMetrics(b *strings.Builder, driverStats map[string]core.DriverStatus) {
	if len(driverStats) == 0 {
		return
	}
	names := sortedKeys(driverStats)

	writePromHeader(b, "corec_driver_read_total", "counter", "Total reads performed by this driver")
	for _, name := range names {
		d := driverStats[name]
		fmt.Fprintf(b, "corec_driver_read_total{driver=%q,type=%q} %d\n",
			promEscape(name), promEscape(d.Type), d.ReadCount)
	}

	writePromHeader(b, "corec_driver_errors_total", "counter", "Total errors reported by this driver")
	for _, name := range names {
		d := driverStats[name]
		fmt.Fprintf(b, "corec_driver_errors_total{driver=%q} %d\n",
			promEscape(name), d.ErrorCount)
	}

	writePromHeader(b, "corec_driver_tags", "gauge", "Number of tags configured for this driver")
	for _, name := range names {
		d := driverStats[name]
		fmt.Fprintf(b, "corec_driver_tags{driver=%q} %d\n",
			promEscape(name), d.TagCount)
	}

	writePromHeader(b, "corec_driver_connected", "gauge", "1 if the driver is connected, 0 otherwise")
	for _, name := range names {
		d := driverStats[name]
		val := 0
		if d.State == core.StateConnected {
			val = 1
		}
		fmt.Fprintf(b, "corec_driver_connected{driver=%q} %d\n",
			promEscape(name), val)
	}
}

// writeTransportMetrics emits per-transport counter and gauge families, in
// sorted name order for deterministic output.
func writeTransportMetrics(b *strings.Builder, transportStats map[string]core.TransportStatus) {
	if len(transportStats) == 0 {
		return
	}
	names := sortedKeys(transportStats)

	writePromHeader(b, "corec_transport_published_total", "counter", "Total messages published by this transport")
	for _, name := range names {
		t := transportStats[name]
		fmt.Fprintf(b, "corec_transport_published_total{transport=%q,type=%q} %d\n",
			promEscape(name), promEscape(t.Type), t.Published)
	}

	writePromHeader(b, "corec_transport_failed_total", "counter", "Total publish failures for this transport")
	for _, name := range names {
		t := transportStats[name]
		fmt.Fprintf(b, "corec_transport_failed_total{transport=%q} %d\n",
			promEscape(name), t.Failed)
	}

	writePromHeader(b, "corec_transport_received_total", "counter", "Total data points received by this transport")
	for _, name := range names {
		t := transportStats[name]
		fmt.Fprintf(b, "corec_transport_received_total{transport=%q} %d\n",
			promEscape(name), t.Received)
	}

	writePromHeader(b, "corec_transport_queue_size", "gauge", "Current outbound queue size for this transport")
	for _, name := range names {
		t := transportStats[name]
		fmt.Fprintf(b, "corec_transport_queue_size{transport=%q} %d\n",
			promEscape(name), t.QueueSize)
	}

	writePromHeader(b, "corec_transport_connected", "gauge", "1 if the transport is connected, 0 otherwise")
	for _, name := range names {
		t := transportStats[name]
		val := 0
		if t.State == core.StateConnected {
			val = 1
		}
		fmt.Fprintf(b, "corec_transport_connected{transport=%q} %d\n",
			promEscape(name), val)
	}
}

// writeRuntimeMetrics emits Go runtime and process-level metrics as
// Prometheus gauges. These are essential for production monitoring:
// goroutine leaks, memory pressure, GC pressure, and CPU usage.
func writeRuntimeMetrics(b *strings.Builder) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Goroutines
	writePromHeader(b, "corec_goroutines", "gauge", "Number of running goroutines")
	fmt.Fprintf(b, "corec_goroutines %d\n", runtime.NumGoroutine())

	// Memory: heap in-use
	writePromHeader(b, "corec_mem_heap_alloc_bytes", "gauge", "Bytes of heap memory allocated and still in use")
	fmt.Fprintf(b, "corec_mem_heap_alloc_bytes %d\n", m.HeapAlloc)

	writePromHeader(b, "corec_mem_heap_sys_bytes", "gauge", "Bytes of heap memory obtained from the OS")
	fmt.Fprintf(b, "corec_mem_heap_sys_bytes %d\n", m.HeapSys)

	writePromHeader(b, "corec_mem_stack_inuse_bytes", "gauge", "Bytes of stack memory in use")
	fmt.Fprintf(b, "corec_mem_stack_inuse_bytes %d\n", m.StackInuse)

	// Memory: total alloc (counter-like, but exposed as gauge since it's cumulative)
	writePromHeader(b, "corec_mem_total_alloc_bytes", "gauge", "Total bytes of memory allocated (cumulative)")
	fmt.Fprintf(b, "corec_mem_total_alloc_bytes %d\n", m.TotalAlloc)

	// GC
	writePromHeader(b, "corec_gc_count", "gauge", "Total number of GC completions")
	fmt.Fprintf(b, "corec_gc_count %d\n", m.NumGC)

	writePromHeader(b, "corec_gc_pause_total_seconds", "gauge", "Total GC pause time in seconds")
	fmt.Fprintf(b, "corec_gc_pause_total_seconds %g\n", float64(m.PauseTotalNs)/1e9)

	// CPU: number of logical CPUs available to the process
	writePromHeader(b, "corec_cpu_count", "gauge", "Number of logical CPUs available to the process")
	fmt.Fprintf(b, "corec_cpu_count %d\n", runtime.NumCPU())
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
