package engine

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestBatcherFlushDeferredWhileQueueBacklogged verifies the B1.3 fix at
// the unit level: a periodic flush must not publish the newer partial
// buffer while older full batches are still queued in flushBatches —
// that would reorder batches across the queue. Once the backlog is
// drained, the partial buffer publishes after the older batches.
func TestBatcherFlushDeferredWhileQueueBacklogged(t *testing.T) {
	tr := &flakyTransport{}
	cfg := core.TransportConfig{Name: "test", BatchSize: 3, FlushInterval: "10s"}
	b := newTransportBatcher(tr, cfg, nil, nil, nil, nil, time.Second)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	// White-box setup: drive flush() without starting flushLoop so the
	// queue backlog is deterministic.
	b.ctx = context.Background()
	b.buf.flushBatches = make(chan []core.DataPoint, defaultFlushBatchQueueSize)

	oldBatch := []core.DataPoint{
		{Driver: "plc", Tag: "t", Value: 0.0, Timestamp: time.Now()},
		{Driver: "plc", Tag: "t", Value: 1.0, Timestamp: time.Now()},
		{Driver: "plc", Tag: "t", Value: 2.0, Timestamp: time.Now()},
	}
	b.buf.flushBatches <- oldBatch

	b.buf.mu.Lock()
	b.buf.buffer = []core.DataPoint{{Driver: "plc", Tag: "t", Value: 3.0, Timestamp: time.Now()}}
	b.buf.mu.Unlock()

	// A periodic flush must NOT overtake the older queued batch.
	b.flush()
	if got := len(tr.getPublished()); got != 0 {
		t.Fatalf("flush() published %d points while older batches were queued; want deferred", got)
	}
	b.buf.mu.Lock()
	bufLen := len(b.buf.buffer)
	b.buf.mu.Unlock()
	if bufLen != 1 {
		t.Fatalf("partial buffer = %d points, want 1 (retained for a later flush)", bufLen)
	}

	// Once the backlog is drained (as the ticker branch does), the
	// partial buffer publishes — after the older batch.
	b.drainFlushBatches()
	b.flush()

	pub := tr.getPublished()
	if len(pub) != 4 {
		t.Fatalf("expected 4 published points, got %d", len(pub))
	}
	for i, p := range pub {
		if p.Value != float64(i) {
			t.Errorf("publish order broken: position %d has value %v, want %d", i, p.Value, i)
		}
	}
}

// TestBatcherCrossBatchOrderingUnderBacklog verifies the B1.3 fix
// end-to-end: with a slow transport building a flush-queue backlog
// while the periodic flush keeps ticking, the newer partial batch must
// never be published before the older queued batches. Before the fix
// the flushLoop select could pseudo-randomly pick the ticker branch and
// publish the partial buffer ahead of the backlog.
func TestBatcherCrossBatchOrderingUnderBacklog(t *testing.T) {
	tr := &slowTransport{publishSlow: 120 * time.Millisecond}
	cfg := core.TransportConfig{Name: "test", BatchSize: 3, FlushInterval: "25ms"}
	b := newTransportBatcher(tr, cfg, nil, nil, nil, nil, time.Second)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	defer b.stop()

	// Points 0-5 form two full batches that queue up behind the
	// in-flight publish; points 6-7 form a partial batch in the buffer
	// that only the periodic flush can send.
	for i := 0; i < 8; i++ {
		if err := b.publish(ctx, core.DataPoint{Driver: "plc", Tag: "t", Value: float64(i), Timestamp: time.Now()}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	if !waitFor(5*time.Second, func() bool { return len(tr.getPublished()) == 8 }) {
		t.Fatalf("expected 8 published points, got %d", len(tr.getPublished()))
	}

	pub := tr.getPublished()
	for i, p := range pub {
		if p.Value != float64(i) {
			t.Fatalf("cross-batch ordering broken: position %d has value %v, want %d", i, p.Value, i)
		}
	}
}
