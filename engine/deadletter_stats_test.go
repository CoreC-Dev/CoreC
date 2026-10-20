package engine

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestDeadLetterStats verifies the queue-depth/lifetime-push pair the
// metrics route exposes via core.DeadLetterStatsProvider (audit OBS-3):
// the pushed counter must keep counting even when the bounded queue
// evicts old entries, so "commands keep dying" stays visible after the
// queue saturates.
func TestDeadLetterStats(t *testing.T) {
	e := New().(*CoreCEngine)
	e.writes.deadLetterMaxLen = 3

	for i := 0; i < 5; i++ {
		e.writes.addDeadLetter(core.DeadLetterEntry{
			Command:  core.WriteCommand{Driver: "d", Tag: "t", Value: i},
			Error:    "boom",
			FailedAt: time.Now(),
			Attempts: 3,
		})
	}

	size, pushed := e.DeadLetterStats()
	if size != 3 {
		t.Errorf("queue size = %d, want 3 (bounded)", size)
	}
	if pushed != 5 {
		t.Errorf("pushed total = %d, want 5 (count survives eviction)", pushed)
	}

	// The newest entries are retained (oldest evicted).
	entries := e.DeadLetterEntries()
	if len(entries) != 3 {
		t.Fatalf("entries len = %d, want 3", len(entries))
	}
	if entries[0].Command.Value != 2 || entries[2].Command.Value != 4 {
		t.Errorf("queue kept wrong entries: got values %v, want [2 3 4]",
			[]any{entries[0].Command.Value, entries[1].Command.Value, entries[2].Command.Value})
	}
}

// fakeIngressTransport is a minimal core.Transport whose only extra
// capability is core.IngressDeadLetterSource with a fixed entry set.
type fakeIngressTransport struct {
	entries []core.DeadLetterEntry
}

func (f *fakeIngressTransport) Init(ctx context.Context, config core.TransportConfig) error {
	return nil
}
func (f *fakeIngressTransport) Start(ctx context.Context) error { return nil }
func (f *fakeIngressTransport) Stop() error                     { return nil }
func (f *fakeIngressTransport) Publish(ctx context.Context, point core.DataPoint) error {
	return nil
}
func (f *fakeIngressTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	return nil
}
func (f *fakeIngressTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (f *fakeIngressTransport) OnData() <-chan core.DataPoint       { return nil }
func (f *fakeIngressTransport) Name() string                        { return "unused" }
func (f *fakeIngressTransport) Type() string                        { return "fake" }
func (f *fakeIngressTransport) Status() core.TransportStatus        { return core.TransportStatus{} }
func (f *fakeIngressTransport) CommandDeadLetterEntries() []core.DeadLetterEntry {
	return f.entries
}

// TestIngressDeadLettersAggregation verifies the engine-side aggregation
// behind GET /write/failed?source=ingress (audit RES-1/TRA-4): entries
// from every transport implementing core.IngressDeadLetterSource are
// collected in sorted transport order, each stamped with the transport
// name that dropped it so the operator sees which pipeline stage lost
// the command.
func TestIngressDeadLettersAggregation(t *testing.T) {
	e := New().(*CoreCEngine)
	e.transportMgr.transports["mqtt-b"] = &fakeIngressTransport{entries: []core.DeadLetterEntry{
		{Command: core.WriteCommand{Tag: "b1"}, Error: "command channel full at ingress"},
	}}
	e.transportMgr.transports["mqtt-a"] = &fakeIngressTransport{entries: []core.DeadLetterEntry{
		{Command: core.WriteCommand{Tag: "a1"}, Error: "command channel full at ingress"},
	}}

	got := e.IngressDeadLetters()
	want := []core.DeadLetterEntry{
		{Command: core.WriteCommand{Tag: "a1"}, Error: "command channel full at ingress", Transport: "mqtt-a"},
		{Command: core.WriteCommand{Tag: "b1"}, Error: "command channel full at ingress", Transport: "mqtt-b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("IngressDeadLetters() = %+v, want %+v", got, want)
	}
}

// TestOfflineBufferBytesWatermark verifies the byte-level offline-buffer
// gauge (roadmap #28): after pushing known-sized batches, PendingBytes
// reflects their on-disk footprint, and the engine facade serves the
// same value for /metrics.
func TestOfflineBufferBytesWatermark(t *testing.T) {
	dir := t.TempDir()
	buf, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}

	pts := []core.DataPoint{{
		Driver: "d", Tag: "t", Value: "some-payload-value", Type: core.TypeString,
		Quality: core.QualityGood, Timestamp: time.Now(),
	}}
	if err := buf.Push(pts, "mqtt-a"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	bytes1 := buf.PendingBytes()
	if bytes1 <= 0 {
		t.Fatalf("PendingBytes = %d after one push, want > 0", bytes1)
	}

	// A second batch grows the footprint.
	if err := buf.Push(pts, "mqtt-a"); err != nil {
		t.Fatalf("Push 2: %v", err)
	}
	if bytes2 := buf.PendingBytes(); bytes2 <= bytes1 {
		t.Errorf("PendingBytes = %d after second push, want > %d", bytes2, bytes1)
	}

	// Drain clears it back to zero bytes alongside zero pending.
	if _, err := buf.Drain("mqtt-a", func([]core.DataPoint) error { return nil }); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := buf.PendingBytes(); got != 0 {
		t.Errorf("PendingBytes after drain = %d, want 0", got)
	}
}
