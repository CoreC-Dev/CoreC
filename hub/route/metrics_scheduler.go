package route

import (
	"fmt"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
)

// writeSchedulerMetrics emits scheduler health families from the
// engine's optional core.SchedulerHealthProvider role (audit finding
// OBS-5 / roadmap #7). Degraded collections, sustained error streaks,
// and cumulative overruns were previously visible only in throttled
// logs — with these gauges Prometheus can alert on "this driver is
// running degraded" and "reads are consistently slower than the
// collection interval" without shipping logs.
//
// When the engine does not satisfy the role (test mocks, engines that
// never started a scheduler), no families are emitted.
func writeSchedulerMetrics(b *strings.Builder) {
	provider, ok := getEngine().(core.SchedulerHealthProvider)
	if !ok || provider == nil {
		return
	}
	health := provider.SchedulerHealth()
	if len(health) == 0 {
		return
	}

	writePromHeader(b, "corec_scheduler_degraded", "gauge", "1 if any of this driver's collection tasks is auto-degraded (interval stretched after sustained read failures), 0 otherwise")
	for _, h := range health {
		val := 0
		if h.Degraded {
			val = 1
		}
		fmt.Fprintf(b, "corec_scheduler_degraded{driver=\"%s\"} %d\n",
			promEscape(h.Driver), val)
	}

	writePromHeader(b, "corec_scheduler_consecutive_errors", "gauge", "Worst streak of consecutive read failures across this driver's collection tasks (resets on the first success)")
	for _, h := range health {
		fmt.Fprintf(b, "corec_scheduler_consecutive_errors{driver=\"%s\"} %d\n",
			promEscape(h.Driver), h.ConsecutiveErrors)
	}

	writePromHeader(b, "corec_scheduler_overruns_total", "counter", "Total reads whose latency exceeded the collection interval, per driver (cumulative over the process lifetime, never reset)")
	for _, h := range health {
		fmt.Fprintf(b, "corec_scheduler_overruns_total{driver=\"%s\"} %d\n",
			promEscape(h.Driver), h.OverrunsTotal)
	}
}
