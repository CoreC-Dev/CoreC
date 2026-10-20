package metrics

import (
	"reflect"
	"sync"
	"testing"
)

func TestRegistryCounterAccumulates(t *testing.T) {
	r := NewRegistry()
	r.IncCounter("modbus_exception_total", map[string]string{"code": "illegal-data-address"})
	r.IncCounter("modbus_exception_total", map[string]string{"code": "illegal-data-address"})
	r.IncCounter("modbus_exception_total", map[string]string{"code": "server-device-failure"})

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("series = %d, want 2 (label sets split)", len(snap))
	}
	// Sorted by name then labels: illegal-data-address < server-device-failure.
	if snap[0].Value != 2 || snap[1].Value != 1 {
		t.Errorf("values = %v/%v, want 2/1", snap[0].Value, snap[1].Value)
	}
	for _, m := range snap {
		if m.Kind != KindCounter {
			t.Errorf("kind = %q, want counter", m.Kind)
		}
	}
}

func TestRegistryGaugeOverwrites(t *testing.T) {
	r := NewRegistry()
	r.SetGauge("opcua_keepalive", 5, nil)
	r.SetGauge("opcua_keepalive", 3, nil)
	if snap := r.Snapshot(); len(snap) != 1 || snap[0].Value != 3 {
		t.Errorf("gauge = %+v, want a single value 3 (overwrite, not accumulate)", snap)
	}
}

func TestRegistryLabelOrderIrrelevant(t *testing.T) {
	r := NewRegistry()
	r.IncCounter("m", map[string]string{"a": "1", "b": "2"})
	r.IncCounter("m", map[string]string{"b": "2", "a": "1"})
	if snap := r.Snapshot(); len(snap) != 1 || snap[0].Value != 2 {
		t.Errorf("series = %+v, want one accumulated series of 2", snap)
	}
}

func TestRegistryConcurrentWrites(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.IncCounter("hot", map[string]string{"w": "x"})
			}
		}()
	}
	wg.Wait()
	if snap := r.Snapshot(); len(snap) != 1 || snap[0].Value != 800 {
		t.Errorf("concurrent total = %v, want 800", snap)
	}
}

func TestRegistrySnapshotDeterministic(t *testing.T) {
	build := func() []Metric {
		r := NewRegistry()
		r.IncCounter("b_metric", nil)
		r.IncCounter("a_metric", map[string]string{"z": "1"})
		r.IncCounter("a_metric", map[string]string{"a": "1"})
		r.SetGauge("a_metric", 9, map[string]string{"m": "2"})
		return r.Snapshot()
	}
	if !reflect.DeepEqual(build(), build()) {
		t.Error("snapshot order not deterministic")
	}
}
