package route

import (
	"fmt"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
)

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

	writePromHeader(b, "corec_transport_dropped_commands_total", "counter", "Write commands dropped at ingress because the command channel was full")
	for _, name := range names {
		t := transportStats[name]
		if t.DroppedCommands > 0 {
			fmt.Fprintf(b, "corec_transport_dropped_commands_total{transport=%q} %d\n",
				promEscape(name), t.DroppedCommands)
		}
	}
}
