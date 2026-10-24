package route

import (
	"fmt"
	"strings"
	"time"

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
		fmt.Fprintf(b, "corec_driver_read_total{driver=\"%s\",type=\"%s\"} %d\n",
			promEscape(name), promEscape(d.Type), d.ReadCount)
	}

	writePromHeader(b, "corec_driver_errors_total", "counter", "Total errors reported by this driver")
	for _, name := range names {
		d := driverStats[name]
		fmt.Fprintf(b, "corec_driver_errors_total{driver=\"%s\"} %d\n",
			promEscape(name), d.ErrorCount)
	}

	writePromHeader(b, "corec_driver_reconnect_total", "counter", "Total reconnect attempts made by this driver")
	for _, name := range names {
		d := driverStats[name]
		fmt.Fprintf(b, "corec_driver_reconnect_total{driver=\"%s\"} %d\n",
			promEscape(name), d.ReconnectCount)
	}

	writePromHeader(b, "corec_driver_tags", "gauge", "Number of tags configured for this driver")
	for _, name := range names {
		d := driverStats[name]
		fmt.Fprintf(b, "corec_driver_tags{driver=\"%s\"} %d\n",
			promEscape(name), d.TagCount)
	}

	writePromHeader(b, "corec_driver_connected", "gauge", "1 if the driver is connected, 0 otherwise")
	for _, name := range names {
		d := driverStats[name]
		val := 0
		if d.State == core.StateConnected {
			val = 1
		}
		fmt.Fprintf(b, "corec_driver_connected{driver=\"%s\"} %d\n",
			promEscape(name), val)
	}

	// Freshness observability:
	// the driver status carries last_read, but only through
	// the JSON API - Prometheus could not alert on "driver N has not
	// read for M seconds" without polling /drivers. Both gauges are
	// omitted for a driver that has never completed a read rather than
	// fabricating a zero age, which would read as "read just now".
	writePromHeader(b, "corec_driver_last_read_timestamp_seconds", "gauge", "Unix timestamp (seconds) of the driver's last successful read; omitted before the first read")
	for _, name := range names {
		d := driverStats[name]
		if d.LastRead.IsZero() {
			continue
		}
		fmt.Fprintf(b, "corec_driver_last_read_timestamp_seconds{driver=\"%s\"} %d\n",
			promEscape(name), d.LastRead.Unix())
	}

	writePromHeader(b, "corec_driver_last_read_seconds", "gauge", "Seconds since the driver's last successful read; use for staleness alerting (e.g. > 2x collection interval means the driver went quiet); omitted before the first read")
	now := time.Now()
	for _, name := range names {
		d := driverStats[name]
		if d.LastRead.IsZero() {
			continue
		}
		fmt.Fprintf(b, "corec_driver_last_read_seconds{driver=\"%s\"} %g\n",
			promEscape(name), now.Sub(d.LastRead).Seconds())
	}
}
