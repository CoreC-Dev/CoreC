package route

import (
	"fmt"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
)

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

	writePromHeader(b, "corec_driver_reconnect_total", "counter", "Total reconnect attempts made by this driver")
	for _, name := range names {
		d := driverStats[name]
		fmt.Fprintf(b, "corec_driver_reconnect_total{driver=%q} %d\n",
			promEscape(name), d.ReconnectCount)
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
