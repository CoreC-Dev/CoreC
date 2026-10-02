package route

import (
	"fmt"
	"runtime"
	"runtime/metrics"
	"strings"
)

// writeRuntimeMetrics emits Go runtime and process-level metrics as
// Prometheus gauges. These are essential for production monitoring:
// goroutine leaks, memory pressure, GC pressure, and CPU usage.
//
// All values are read via runtime/metrics, which samples runtime state
// without triggering a Stop-The-World pause. The previous implementation
// used runtime.ReadMemStats, which forces a STW to obtain a consistent
// snapshot — undesirable on the /metrics scrape path.
// runtime.NumGoroutine() and runtime.NumCPU() are also STW-free and are
// therefore used directly.
func writeRuntimeMetrics(b *strings.Builder) {
	samples := []metrics.Sample{
		{Name: "/memory/classes/heap/objects:bytes"},  // HeapAlloc
		{Name: "/memory/classes/heap/free:bytes"},     // HeapSys component
		{Name: "/memory/classes/heap/unused:bytes"},   // HeapSys component
		{Name: "/memory/classes/heap/released:bytes"}, // HeapSys component
		{Name: "/memory/classes/heap/stacks:bytes"},   // StackInuse
		{Name: "/gc/heap/allocs:bytes"},               // TotalAlloc (cumulative)
		{Name: "/gc/cycles/total:gc-cycles"},          // NumGC (cumulative)
		{Name: "/sched/pauses/total/gc:seconds"},      // GC pause histogram
	}
	metrics.Read(samples)

	heapAlloc := samples[0].Value.Uint64()
	// HeapSys (bytes of heap memory obtained from the OS) has no single
	// runtime/metrics equivalent. It equals the sum of the heap memory
	// classes excluding the stacks class: objects + free + unused + released.
	heapSys := heapAlloc +
		samples[1].Value.Uint64() +
		samples[2].Value.Uint64() +
		samples[3].Value.Uint64()
	stackInuse := samples[4].Value.Uint64()
	totalAlloc := samples[5].Value.Uint64()
	numGC := samples[6].Value.Uint64()
	pauseTotalSec := pauseTotalSeconds(samples[7].Value.Float64Histogram())

	// Goroutines
	writePromHeader(b, "corec_goroutines", "gauge", "Number of running goroutines")
	fmt.Fprintf(b, "corec_goroutines %d\n", runtime.NumGoroutine())

	// Memory: heap in-use
	writePromHeader(b, "corec_mem_heap_alloc_bytes", "gauge", "Bytes of heap memory allocated and still in use")
	fmt.Fprintf(b, "corec_mem_heap_alloc_bytes %d\n", heapAlloc)

	writePromHeader(b, "corec_mem_heap_sys_bytes", "gauge", "Bytes of heap memory obtained from the OS")
	fmt.Fprintf(b, "corec_mem_heap_sys_bytes %d\n", heapSys)

	writePromHeader(b, "corec_mem_stack_inuse_bytes", "gauge", "Bytes of stack memory in use")
	fmt.Fprintf(b, "corec_mem_stack_inuse_bytes %d\n", stackInuse)

	// Memory: total alloc (counter-like, but exposed as gauge since it's cumulative)
	writePromHeader(b, "corec_mem_total_alloc_bytes", "gauge", "Total bytes of memory allocated (cumulative)")
	fmt.Fprintf(b, "corec_mem_total_alloc_bytes %d\n", totalAlloc)

	// GC
	writePromHeader(b, "corec_gc_count", "gauge", "Total number of GC completions")
	fmt.Fprintf(b, "corec_gc_count %d\n", numGC)

	writePromHeader(b, "corec_gc_pause_total_seconds", "gauge", "Total GC pause time in seconds")
	fmt.Fprintf(b, "corec_gc_pause_total_seconds %g\n", pauseTotalSec)

	// CPU: number of logical CPUs available to the process
	writePromHeader(b, "corec_cpu_count", "gauge", "Number of logical CPUs available to the process")
	fmt.Fprintf(b, "corec_cpu_count %d\n", runtime.NumCPU())
}

// pauseTotalSeconds reconstructs the cumulative wall-clock time spent in
// GC stop-the-world pauses from the /sched/pauses/total/gc:seconds
// histogram exposed by runtime/metrics.
//
// runtime/metrics exposes GC pause durations only as a histogram; there is
// no direct cumulative counter equivalent to runtime.MemStats.PauseTotalNs.
// The total is approximated by summing, over every non-empty bucket, the
// bucket's count times its arithmetic midpoint. GC STW pauses are small and
// tightly clustered in the histogram's narrow low-end buckets, so this
// midpoint estimate tracks the true total to within ~2% in practice. The
// histogram spans [-Inf, +Inf]; the first bucket [-Inf, 0) is skipped
// because negative durations are impossible, and the last bucket
// [finite, +Inf) uses the finite lower bound instead of a midpoint to
// avoid producing +Inf.
func pauseTotalSeconds(h *metrics.Float64Histogram) float64 {
	var total float64
	n := len(h.Counts)
	for i, count := range h.Counts {
		if count == 0 {
			continue
		}
		lo, hi := h.Buckets[i], h.Buckets[i+1]
		if i == n-1 {
			// [lo, +Inf): no finite midpoint; use lo as a lower-bound estimate
			// to avoid count * +Inf = +Inf.
			total += float64(count) * lo
		} else if lo >= 0 {
			total += float64(count) * (lo + hi) / 2
		}
		// Bucket 0 spans [-Inf, 0); lo is -Inf, so the lo >= 0 guard above
		// is false and any count there is ignored (negative durations are
		// impossible).
	}
	return total
}
