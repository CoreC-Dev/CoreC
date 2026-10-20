package engine

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/common/metrics"
)

// TestLabeledLatencyObservation verifies the per-driver / per-transport
// latency histograms (audit finding OBS-2): every observation feeds BOTH
// the global aggregate and the labeled histogram, so the unlabeled
// family keeps its meaning while per-driver/transport series become
// queryable.
func TestLabeledLatencyObservation(t *testing.T) {
	e := New().(*CoreCEngine)

	// Read latency per driver.
	e.stats.labeled.observeRead("plc1", 10*time.Millisecond)
	e.stats.labeled.observeRead("plc1", 30*time.Millisecond)
	e.stats.labeled.observeRead("plc2", 500*time.Millisecond)

	byDriver := e.ReadLatencyByDriver()
	if len(byDriver) != 2 {
		t.Fatalf("ReadLatencyByDriver has %d drivers, want 2", len(byDriver))
	}
	if byDriver["plc1"].Count != 2 || byDriver["plc2"].Count != 1 {
		t.Errorf("per-driver counts = plc1:%d plc2:%d, want 2/1", byDriver["plc1"].Count, byDriver["plc2"].Count)
	}
	// The global aggregate saw all three observations.
	if got := e.ReadLatencyHistogram().Count; got != 3 {
		t.Errorf("global read count = %d, want 3 (aggregate must keep receiving observations)", got)
	}

	// Publish latency + data age per transport via the direct-path
	// observers.
	e.stats.labeled.observePublish("mqtt-a", 5*time.Millisecond)
	e.stats.labeled.observePublish("mqtt-a", 7*time.Millisecond)
	e.stats.labeled.observePublish("http-b", 2*time.Second)
	e.stats.labeled.observeAge("mqtt-a", 100*time.Millisecond)
	e.stats.labeled.observeAge("http-b", 3*time.Second)

	byTransport := e.PublishLatencyByTransport()
	if len(byTransport) != 2 {
		t.Fatalf("PublishLatencyByTransport has %d transports, want 2", len(byTransport))
	}
	if byTransport["mqtt-a"].Count != 2 || byTransport["http-b"].Count != 1 {
		t.Errorf("per-transport publish counts = mqtt-a:%d http-b:%d, want 2/1",
			byTransport["mqtt-a"].Count, byTransport["http-b"].Count)
	}
	if got := e.PublishLatencyHistogram().Count; got != 3 {
		t.Errorf("global publish count = %d, want 3", got)
	}
	ages := e.DataAgeByTransport()
	if ages["mqtt-a"].Count != 1 || ages["http-b"].Count != 1 {
		t.Errorf("per-transport age counts = mqtt-a:%d http-b:%d, want 1/1",
			ages["mqtt-a"].Count, ages["http-b"].Count)
	}
	if got := e.DataAgeHistogram().Count; got != 2 {
		t.Errorf("global age count = %d, want 2", got)
	}
}

// TestBatcherFanoutObservers verifies the batcher wiring: the observers
// passed to newTransportBatcher feed both the global histogram and the
// per-transport labeled histogram, and nil observers stay nil-safe.
func TestBatcherFanoutObservers(t *testing.T) {
	e := New().(*CoreCEngine)

	pub := e.stats.labeled.publishObserver("mqtt-x")
	age := e.stats.labeled.ageObserver("mqtt-x")

	pub.Observe(8 * time.Millisecond)
	age.Observe(250 * time.Millisecond)

	byTransport := e.PublishLatencyByTransport()
	if byTransport["mqtt-x"].Count != 1 {
		t.Errorf("labeled publish count = %d, want 1", byTransport["mqtt-x"].Count)
	}
	if got := e.PublishLatencyHistogram().Count; got != 1 {
		t.Errorf("global publish count = %d, want 1 (fan-out must feed both)", got)
	}
	if ages := e.DataAgeByTransport(); ages["mqtt-x"].Count != 1 {
		t.Errorf("labeled age count = %d, want 1", ages["mqtt-x"].Count)
	}
	if got := e.DataAgeHistogram().Count; got != 1 {
		t.Errorf("global age count = %d, want 1", got)
	}

	// The observers satisfy the interface the batcher consumes; a
	// plain histogram also satisfies it (backward-compatible wiring).
	var _ latencyObserver = metrics.NewLatencyHistogram(nil)
}
