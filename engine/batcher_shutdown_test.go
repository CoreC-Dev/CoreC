package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// ctxAwaitTransport is a mock Transport whose PublishBatch blocks until
// the context is cancelled and then fails, simulating a transport stuck
// on a slow/broken broker while still respecting context cancellation.
type ctxAwaitTransport struct {
	name     string
	attempts int
}

func (t *ctxAwaitTransport) Init(_ context.Context, cfg core.TransportConfig) error {
	t.name = cfg.Name
	return nil
}
func (t *ctxAwaitTransport) Start(_ context.Context) error { return nil }
func (t *ctxAwaitTransport) Stop() error                   { return nil }
func (t *ctxAwaitTransport) Publish(ctx context.Context, point core.DataPoint) error {
	return t.PublishBatch(ctx, []core.DataPoint{point})
}
func (t *ctxAwaitTransport) PublishBatch(ctx context.Context, _ []core.DataPoint) error {
	t.attempts++
	<-ctx.Done()
	return ctx.Err()
}
func (t *ctxAwaitTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (t *ctxAwaitTransport) OnData() <-chan core.DataPoint       { return nil }
func (t *ctxAwaitTransport) Name() string                        { return t.name }
func (t *ctxAwaitTransport) Type() string                        { return "ctxawait" }
func (t *ctxAwaitTransport) Status() core.TransportStatus {
	return core.TransportStatus{Name: t.name, Type: "ctxawait", State: core.StateConnected}
}

// ctxRecordingTransport records whether the context of each PublishBatch
// call was already cancelled: cancelled contexts fail (batch retained on
// disk), live contexts publish (batch removed).
type ctxRecordingTransport struct {
	name      string
	mu        sync.Mutex
	cancelled bool
	published int
}

func (t *ctxRecordingTransport) Init(_ context.Context, cfg core.TransportConfig) error {
	t.name = cfg.Name
	return nil
}
func (t *ctxRecordingTransport) Start(_ context.Context) error { return nil }
func (t *ctxRecordingTransport) Stop() error                   { return nil }
func (t *ctxRecordingTransport) Publish(ctx context.Context, point core.DataPoint) error {
	return t.PublishBatch(ctx, []core.DataPoint{point})
}
func (t *ctxRecordingTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		t.cancelled = true
		return err
	}
	t.published += len(points)
	return nil
}
func (t *ctxRecordingTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (t *ctxRecordingTransport) OnData() <-chan core.DataPoint       { return nil }
func (t *ctxRecordingTransport) Name() string                        { return t.name }
func (t *ctxRecordingTransport) Type() string                        { return "ctxrecording" }
func (t *ctxRecordingTransport) Status() core.TransportStatus {
	return core.TransportStatus{Name: t.name, Type: "ctxrecording", State: core.StateConnected}
}

func (t *ctxRecordingTransport) sawCancelled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelled
}

// TestBatcherFlushFinalBudgetPersistsToOffline verifies the B3.1 fix:
// flushFinal publishes on a context bounded by the batcher's shutdown
// budget, so a shutdown flush against a hanging transport stops when
// the budget expires and persists the batch to the offline buffer
// instead of being abandoned mid-retry (or worse, killed by os.Exit
// after the engine already gave up waiting).
func TestBatcherFlushFinalBudgetPersistsToOffline(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	tr := &ctxAwaitTransport{}
	cfg := core.TransportConfig{
		Name:       "test",
		BatchSize:  100, // partial batch stays in the buffer until stop()
		RetryCount: 6,   // without the budget this chain alone far exceeds it
	}
	budget := 200 * time.Millisecond
	b := newTransportBatcher(tr, cfg, ob, nil, nil, nil, budget)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)

	// A partial batch sits in the buffer; stop() must flushFinal it.
	if err := b.publish(ctx, core.DataPoint{Driver: "plc", Tag: "t", Value: 1.0, Timestamp: time.Now()}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	start := time.Now()
	b.stop()
	elapsed := time.Since(start)

	// The flush must not run the full retry chain (7 attempts × 10s
	// attempt timeouts + backoff); it must end within the budget, plus
	// slack for the offline-buffer disk write.
	if elapsed > budget+2*time.Second {
		t.Fatalf("stop() took %v; flushFinal ignored its %v shutdown budget", elapsed, budget)
	}
	if got := ob.Len(); got != 1 {
		t.Fatalf("offline buffer Len = %d, want 1 (batch persisted after budget expiry)", got)
	}
	if tr.attempts < 1 {
		t.Fatalf("expected at least one publish attempt, got %d", tr.attempts)
	}
}

// TestBatcherDrainOnceInheritsBatcherContext verifies the B3.1 fix: the
// drainOnce replay callback derives its per-batch context from b.ctx,
// so an in-flight replay is aborted promptly once the batcher stops.
// Before the fix the callback used context.Background(), so shutdown
// could not cancel an in-flight replay at all.
func TestBatcherDrainOnceInheritsBatcherContext(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	points := []core.DataPoint{{Driver: "plc", Tag: "t", Value: 7.0, Timestamp: time.Now()}}
	if err := ob.Push(points, "test"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	tr := &ctxRecordingTransport{name: "test"}
	cfg := core.TransportConfig{Name: "test", BatchSize: 10, FlushInterval: "10s"}
	b := newTransportBatcher(tr, cfg, ob, nil, nil, nil, time.Second)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.start(ctx)

	// Simulate shutdown cancelling the batcher context while a drain
	// tick is in flight (drainLoop may be mid-iteration when stop()
	// cancels b.ctx).
	cancel()
	b.drainOnce()

	// The replay must observe the cancelled context and fail, leaving
	// the batch on disk for replay after restart.
	if !tr.sawCancelled() {
		t.Fatal("expected drainOnce replay to inherit the cancelled batcher context")
	}
	if got := ob.Len(); got != 1 {
		t.Fatalf("offline buffer Len = %d, want 1 (replay aborted, batch retained)", got)
	}
}

// TestRemoveTransportBoundsBatcherStop verifies the B3.1/B3.6 fix: the
// batcher stop on the RemoveTransport path is bounded by the engine's
// shutdown timeout instead of blocking the management API for the whole
// flush retry chain.
func TestRemoveTransportBoundsBatcherStop(t *testing.T) {
	eng := New().(*CoreCEngine)
	eng.shutdownTimeout = 50 * time.Millisecond

	tr := &ctxAwaitTransport{name: "slow"}
	cfg := core.TransportConfig{Name: "slow", BatchSize: 10, RetryCount: 3}
	// Give the batcher a flushFinal budget far beyond the engine
	// timeout: the abandoned stop goroutine finishes on its own a few
	// hundred milliseconds later.
	b := newTransportBatcher(tr, cfg, nil, nil, nil, nil, 300*time.Millisecond)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	eng.mu.Lock()
	eng.transportMgr.transports["slow"] = tr
	eng.transportMgr.batchers["slow"] = b
	eng.mu.Unlock()

	// Seed the buffer so stop() has a flushFinal to run.
	bctx, bcancel := context.WithCancel(context.Background())
	b.start(bctx)
	defer bcancel()
	if err := b.publish(bctx, core.DataPoint{Driver: "plc", Tag: "t", Value: 1.0, Timestamp: time.Now()}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	start := time.Now()
	if err := eng.RemoveTransport("slow"); err != nil {
		t.Fatalf("RemoveTransport: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 250*time.Millisecond {
		t.Fatalf("RemoveTransport blocked %v on the batcher stop; want bounded by the 50ms engine shutdown timeout", elapsed)
	}
}

// TestBatcherPublishRejectedAfterStop verifies the B1.6/B3.4 fix: after
// stop(), publish() rejects new points (counted + logged) instead of
// letting them strand silently in the buffer or the flush queue after
// the final flush has already run.
func TestBatcherPublishRejectedAfterStop(t *testing.T) {
	tr := &flakyTransport{}
	cfg := core.TransportConfig{Name: "test", BatchSize: 100, FlushInterval: "10s"}
	b := newTransportBatcher(tr, cfg, nil, nil, nil, nil, time.Second)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	b.stop()

	err := b.publish(ctx, core.DataPoint{Driver: "plc", Tag: "t", Value: 1.0, Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected publish() after stop to return an error")
	}
	if got := b.buf.publishRejected.Load(); got != 1 {
		t.Fatalf("publishRejected = %d, want 1", got)
	}

	// The rejected point must not sit in the buffer.
	b.buf.mu.Lock()
	bufLen := len(b.buf.buffer)
	b.buf.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("rejected point stranded in buffer: len=%d", bufLen)
	}
}

// TestBatcherPublishRejectedWhileStopping verifies the rejection takes
// effect from the very first step of stop(), not only after it
// completes: stop() sets the stopped flag before cancelling its loops,
// so a worker racing with shutdown gets a counted, logged rejection
// instead of silently stranding its point (B1.6/B3.4).
func TestBatcherPublishRejectedWhileStopping(t *testing.T) {
	tr := &ctxAwaitTransport{}
	cfg := core.TransportConfig{Name: "test", BatchSize: 100, RetryCount: 3}
	b := newTransportBatcher(tr, cfg, nil, nil, nil, nil, 300*time.Millisecond)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	// A partial batch keeps stop() busy in flushFinal (the hanging
	// transport holds it until the 300ms budget expires).
	if err := b.publish(ctx, core.DataPoint{Driver: "plc", Tag: "t", Value: 1.0, Timestamp: time.Now()}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	done := make(chan struct{})
	go func() {
		b.stop()
		close(done)
	}()

	// Wait until stop() has begun — the stopped flag is its first step.
	if !waitFor(time.Second, func() bool { return b.buf.stopped.Load() }) {
		t.Fatal("stop() did not set the stopped flag")
	}

	if err := b.publish(ctx, core.DataPoint{Driver: "plc", Tag: "t", Value: 2.0, Timestamp: time.Now()}); err == nil {
		t.Fatal("expected publish() rejection while stop() is in flight")
	}
	if got := b.buf.publishRejected.Load(); got != 1 {
		t.Fatalf("publishRejected = %d, want 1", got)
	}

	// stop() must still complete on its own (within its budget).
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop() did not finish within its shutdown budget")
	}
}

// TestStopComponentsDropsAndCountsQueuedPoints verifies the B3.4
// visibility fix: points still queued in the data bus when the batchers
// are about to be stopped are drained, counted as errors and logged,
// instead of being silently abandoned when the bus is closed.
func TestStopComponentsDropsAndCountsQueuedPoints(t *testing.T) {
	eng := New().(*CoreCEngine)
	eng.dataBus = NewDataBus(64)
	for i := 0; i < 3; i++ {
		eng.dataBus.Push(core.DataPoint{Driver: "plc", Tag: "t", Value: float64(i), Timestamp: time.Now()})
	}
	for i := 0; i < 2; i++ {
		eng.dataBus.PushHighPriority(core.DataPoint{Driver: "plc", Tag: "hp", Value: float64(i), Timestamp: time.Now()})
	}

	eng.stopComponents()

	if got := eng.stats.totalErrors.Load(); got != 5 {
		t.Fatalf("totalErrors = %d, want 5 (3 main + 2 high-priority queued points dropped and counted)", got)
	}
	if got := eng.stats.statManager.Snapshot().ErrorTotal; got != 5 {
		t.Fatalf("statManager errors = %d, want 5", got)
	}
	// The bus must be closed (both channels report closed afterwards).
	select {
	case _, ok := <-eng.dataBus.Channel():
		if ok {
			t.Fatal("expected the main data bus channel to be closed after stopComponents")
		}
	default:
		t.Fatal("expected the main data bus channel to be closed after stopComponents")
	}
}
