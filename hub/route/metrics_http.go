package route

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// writeHTTPMetrics emits HTTP request counts and a request-duration
// histogram from the package-level httpMetricsCollector populated by
// httpMetricsMiddleware.
func writeHTTPMetrics(b *strings.Builder) {
	counts, durBuckets, durSum, durCount := httpMetricsCollector.snapshot()

	// ── Request count by method + status ──
	writePromHeader(b, "corec_http_requests_total", "counter", "Total HTTP requests served by the API gateway")
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// key format: "METHOD:STATUS"
		method, status, ok := splitMetricKey(k)
		if !ok {
			continue
		}
		fmt.Fprintf(b, "corec_http_requests_total{method=%q,status=%q} %d\n",
			promEscape(method), promEscape(status), counts[k])
	}

	// ── Request duration histogram ──
	// durBuckets are non-cumulative per-bucket counts; Prometheus expects
	// cumulative buckets. We compute the running sum below.
	writePromHeader(b, "corec_http_request_duration_seconds", "histogram", "HTTP request duration in seconds")
	cumulative := uint64(0)
	for i, ub := range defaultHTTPDurationBuckets {
		cumulative += durBuckets[i]
		fmt.Fprintf(b, "corec_http_request_duration_seconds_bucket{le=%q} %d\n",
			strconv.FormatFloat(ub, 'f', -1, 64), cumulative)
	}
	// +Inf bucket = total count
	cumulative += durBuckets[len(defaultHTTPDurationBuckets)] // overflow bucket
	fmt.Fprintf(b, "corec_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cumulative)
	fmt.Fprintf(b, "corec_http_request_duration_seconds_sum %g\n", durSum)
	fmt.Fprintf(b, "corec_http_request_duration_seconds_count %d\n", durCount)
}

// splitMetricKey splits "METHOD:STATUS" into its parts.
func splitMetricKey(k string) (method, status string, ok bool) {
	idx := strings.Index(k, ":")
	if idx < 0 {
		return "", "", false
	}
	return k[:idx], k[idx+1:], true
}
