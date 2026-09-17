package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// flakyTransport is a mock Transport whose PublishBatch can be controlled
// to succeed or fail. It records all successfully published points.
type flakyTransport struct {
	name      string
	published []core.DataPoint
	mu        sync.Mutex
	pubCount  atomic.Int64
	failCount atomic.Int64
	// failNext makes the next PublishBatch call return err.
	failNext atomic.Bool
	// alwaysFail makes all PublishBatch calls return err.
	alwaysFail atomic.Bool
}

func (t *flakyTransport) Init(_ context.Context, cfg core.TransportConfig) error {
	t.name = cfg.Name
	return nil
}
func (t *flakyTransport) Start(_ context.Context) error { return nil }
func (t *flakyTransport) Stop() error                   { return nil }
func (t *flakyTransport) Publish(ctx context.Context, point core.DataPoint) error {
	return t.PublishBatch(ctx, []core.DataPoint{point})
}
func (t *flakyTransport) PublishBatch(_ context.Context, points []core.DataPoint) error {
	if t.alwaysFail.Load() {
		t.failCount.Add(1)
		return errors.New("transport unavailable")
	}
	if t.failNext.Load() {
		t.failNext.Store(false)
		t.failCount.Add(1)
		return errors.New("transient publish error")
	}
	t.mu.Lock()
	t.published = append(t.published, points...)
	t.mu.Unlock()
	t.pubCount.Add(int64(len(points)))
	return nil
}
func (t *flakyTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (t *flakyTransport) OnData() <-chan core.DataPoint       { return nil }
func (t *flakyTransport) Name() string                        { return t.name }
func (t *flakyTransport) Type() string                        { return "flaky" }
func (t *flakyTransport) Status() core.TransportStatus {
	return core.TransportStatus{Name: t.name, Type: "flaky", State: core.StateConnected}
}

func (t *flakyTransport) getPublished() []core.DataPoint {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]core.DataPoint, len(t.published))
	copy(cp, t.published)
	return cp
}

// --- Tests ---

// TestBatcherPublishFlush verifies that publish() buffers points and
// flushes when the buffer reaches batchSize, delivering them via
// PublishBatch.
func TestBatcherPublishFlush(t *testing.T) {
	tr := &flakyTransport{}
	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     3,
		FlushInterval: "10s", // long interval; we trigger flush via batch size
	}
	b := newTransportBatcher(tr, cfg, nil)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	defer b.stop()

	// Send 3 points — should trigger a flush.
	for i := 0; i < 3; i++ {
		_ = b.publish(ctx, core.DataPoint{
			Driver: "plc", Tag: "t", Value: float64(i), Timestamp: time.Now(),
		})
	}

	// Wait for the published points to appear.
	waitFor := func(timeout time.Duration, check func() bool) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if check() {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}
	if !waitFor(time.Second, func() bool { return tr.pubCount.Load() == 3 }) {
		t.Fatalf("expected 3 published, got %d", tr.pubCount.Load())
	}

	pts := tr.getPublished()
	if len(pts) != 3 {
		t.Fatalf("expected 3 points, got %d", len(pts))
	}
	for i, p := range pts {
		if p.Value != float64(i) {
			t.Errorf("point %d: expected value %v, got %v", i, float64(i), p.Value)
		}
	}
}

// TestBatcherFlushInterval verifies that the periodic flush loop
// delivers buffered points without filling the batch.
func TestBatcherFlushInterval(t *testing.T) {
	tr := &flakyTransport{}
	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     100, // large; won't fill
		FlushInterval: "50ms",
	}
	b := newTransportBatcher(tr, cfg, nil)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	defer b.stop()

	_ = b.publish(ctx, core.DataPoint{
		Driver: "plc", Tag: "t", Value: 42.0, Timestamp: time.Now(),
	})

	// Wait for periodic flush.
	waitFor := func(timeout time.Duration, check func() bool) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if check() {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}
	if !waitFor(2*time.Second, func() bool { return tr.pubCount.Load() >= 1 }) {
		t.Fatalf("expected >=1 published via interval flush, got %d", tr.pubCount.Load())
	}
}

// TestBatcherRetryThenSucceed verifies that publishWithRetryAndBuffer
// retries on failure and eventually succeeds.
func TestBatcherRetryThenSucceed(t *testing.T) {
	tr := &flakyTransport{}
	tr.failNext.Store(true) // first call fails, retry succeeds

	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     1, // immediate flush
		FlushInterval: "10s",
		RetryCount:    2,
	}
	b := newTransportBatcher(tr, cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	defer b.stop()

	_ = b.publish(ctx, core.DataPoint{
		Driver: "plc", Tag: "t", Value: 1.0, Timestamp: time.Now(),
	})

	waitFor := func(timeout time.Duration, check func() bool) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if check() {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}
	// Should have retried and eventually published.
	if !waitFor(5*time.Second, func() bool { return tr.pubCount.Load() == 1 }) {
		t.Fatalf("expected 1 published after retry, got %d (failCount=%d)", tr.pubCount.Load(), tr.failCount.Load())
	}
	if tr.failCount.Load() != 1 {
		t.Errorf("expected 1 failed attempt, got %d", tr.failCount.Load())
	}
}

// TestBatcherRetryExhaustedBuffersToOffline verifies that when all retries
// are exhausted and an offline buffer is configured, the batch is persisted
// to disk for later replay.
func TestBatcherRetryExhaustedBuffersToOffline(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	tr := &flakyTransport{}
	tr.alwaysFail.Store(true) // all publish calls fail

	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     2,
		FlushInterval: "10s",
		RetryCount:    1, // 1 retry = 2 total attempts
	}
	b := newTransportBatcher(tr, cfg, ob)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	defer b.stop()

	// Send 2 points to fill the batch and trigger flush.
	for i := 0; i < 2; i++ {
		_ = b.publish(ctx, core.DataPoint{
			Driver: "plc", Tag: "t", Value: float64(i), Timestamp: time.Now(),
		})
	}

	// Wait for the batch to be buffered to offline storage.
	waitFor := func(timeout time.Duration, check func() bool) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if check() {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}
	// RetryCount=1 means 2 attempts with 100ms backoff between them.
	// Total time ≈ 100ms + publish attempts. Allow generous timeout.
	if !waitFor(10*time.Second, func() bool { return ob.Len() >= 1 }) {
		t.Fatalf("expected batch in offline buffer, got len=%d (failCount=%d)", ob.Len(), tr.failCount.Load())
	}
	if tr.failCount.Load() < 2 {
		t.Errorf("expected >=2 failed attempts, got %d", tr.failCount.Load())
	}
}

// TestBatcherDrainReplaysOfflineBuffer verifies that drainOnce replays
// buffered batches from the offline buffer and clears them on success.
func TestBatcherDrainReplaysOfflineBuffer(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	// Pre-populate the offline buffer with a batch.
	points := []core.DataPoint{
		{Driver: "plc", Tag: "t1", Value: 10.0, Timestamp: time.Now()},
		{Driver: "plc", Tag: "t2", Value: 20.0, Timestamp: time.Now()},
	}
	if err := ob.Push(points, "test"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if ob.Len() != 1 {
		t.Fatalf("expected ob.Len=1, got %d", ob.Len())
	}

	tr := &flakyTransport{} // succeeds now
	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     10,
		FlushInterval: "10s",
	}
	b := newTransportBatcher(tr, cfg, ob)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	defer b.stop()

	// Call drainOnce directly (it's normally triggered by the drainLoop ticker).
	b.drainOnce()

	// The offline buffer should be empty after successful drain.
	if ob.Len() != 0 {
		t.Fatalf("expected ob.Len=0 after drain, got %d", ob.Len())
	}

	// The transport should have received the replayed points.
	pub := tr.getPublished()
	if len(pub) != 2 {
		t.Fatalf("expected 2 published points, got %d", len(pub))
	}
	if pub[0].Value != 10.0 || pub[1].Value != 20.0 {
		t.Errorf("unexpected values: %v, %v", pub[0].Value, pub[1].Value)
	}
}

// TestBatcherFlushFinalPublishesRemaining verifies that stop() flushes
// any remaining buffered points via flushFinal.
func TestBatcherFlushFinalPublishesRemaining(t *testing.T) {
	tr := &flakyTransport{}
	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     100,   // large; won't fill
		FlushInterval: "10s", // long; won't tick
	}
	b := newTransportBatcher(tr, cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)

	// Publish a point that stays in the buffer (no flush triggered).
	_ = b.publish(ctx, core.DataPoint{
		Driver: "plc", Tag: "t", Value: 99.0, Timestamp: time.Now(),
	})

	// stop() should flush the remaining buffer.
	b.stop()

	// The point should have been published.
	pub := tr.getPublished()
	if len(pub) != 1 {
		t.Fatalf("expected 1 point published on stop, got %d", len(pub))
	}
	if pub[0].Value != 99.0 {
		t.Errorf("expected value 99.0, got %v", pub[0].Value)
	}
}

// TestBatcherOfflineBufferPendingNil verifies the nil-buffer contract: a
// batcher with no offline buffer reports zero for all offline-buffer stats.
func TestBatcherOfflineBufferPendingNil(t *testing.T) {
	tr := &flakyTransport{}
	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     10,
		FlushInterval: "10s",
	}
	b := newTransportBatcher(tr, cfg, nil)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	if got := b.OfflineBufferPending(); got != 0 {
		t.Fatalf("OfflineBufferPending = %d, want 0 (nil buffer)", got)
	}
	if got := b.OfflineBufferDrained(); got != 0 {
		t.Fatalf("OfflineBufferDrained = %d, want 0 (nil buffer)", got)
	}
	if got := b.OfflineBufferPushed(); got != 0 {
		t.Fatalf("OfflineBufferPushed = %d, want 0 (nil buffer)", got)
	}
}

// TestBatcherOfflineBufferPendingWithBuffer verifies that
// OfflineBufferPending reflects the buffer's on-disk count and stays in sync
// as batches are pushed and drained.
func TestBatcherOfflineBufferPendingWithBuffer(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	tr := &flakyTransport{}
	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     10,
		FlushInterval: "10s",
	}
	b := newTransportBatcher(tr, cfg, ob)
	if b == nil {
		t.Fatal("expected non-nil batcher")
	}

	if got := b.OfflineBufferPending(); got != 0 {
		t.Fatalf("OfflineBufferPending = %d, want 0 on empty buffer", got)
	}

	// Pre-populate the buffer outside the batcher.
	points := []core.DataPoint{{Driver: "plc", Tag: "t", Value: 1.0, Timestamp: time.Now()}}
	for i := 0; i < 3; i++ {
		if err := ob.Push(points, "test"); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}
	if got := b.OfflineBufferPending(); got != 3 {
		t.Fatalf("OfflineBufferPending = %d, want 3", got)
	}
	if got := b.OfflineBufferPending(); got != ob.Len() {
		t.Fatalf("OfflineBufferPending (%d) != ob.Len (%d)", got, ob.Len())
	}

	// drainOnce replays the batches; pending should drop to 0 and the
	// drained counter should reflect the replayed batches.
	b.drainOnce()
	if got := b.OfflineBufferPending(); got != 0 {
		t.Fatalf("OfflineBufferPending = %d, want 0 after drain", got)
	}
	if got := b.OfflineBufferDrained(); got != 3 {
		t.Fatalf("OfflineBufferDrained = %d, want 3 after drain", got)
	}
}

// TestBatcherOfflineBufferPushedIncremented verifies that
// offlineBufferPushes is incremented exactly once per batch persisted to the
// offline buffer after retries are exhausted.
func TestBatcherOfflineBufferPushedIncremented(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	tr := &flakyTransport{}
	tr.alwaysFail.Store(true) // all publish calls fail → batches are buffered

	cfg := core.TransportConfig{
		Name:          "test",
		BatchSize:     1, // immediate flush per point
		FlushInterval: "10s",
		RetryCount:    1,
	}
	b := newTransportBatcher(tr, cfg, ob)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.start(ctx)
	defer b.stop()

	// Publish 2 points; each fills the size-1 batch, fails all retries,
	// and is pushed to the offline buffer.
	for i := 0; i < 2; i++ {
		_ = b.publish(ctx, core.DataPoint{
			Driver: "plc", Tag: "t", Value: float64(i), Timestamp: time.Now(),
		})
	}

	waitFor := func(timeout time.Duration, check func() bool) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if check() {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}
	if !waitFor(10*time.Second, func() bool { return b.OfflineBufferPushed() == 2 }) {
		t.Fatalf("OfflineBufferPushed = %d, want 2", b.OfflineBufferPushed())
	}
	if got := b.OfflineBufferPending(); got != 2 {
		t.Fatalf("OfflineBufferPending = %d, want 2", got)
	}
}

// TestEngineOfflineBufferStats verifies the engine-level aggregation:
//   - pushed is summed across batchers (per-batcher counter).
//   - pending and drained are read once from the shared buffer, NOT
//     multiplied by the number of batchers sharing it.
func TestEngineOfflineBufferStats(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	// Two batchers share the SAME offline buffer (mirroring production,
	// where e.offlineBuffer is passed to every newTransportBatcher). Each
	// has pushed a distinct number of batches.
	b1 := &transportBatcher{offlineBuffer: ob}
	b1.offlineBufferPushes.Add(3)
	b2 := &transportBatcher{offlineBuffer: ob}
	b2.offlineBufferPushes.Add(5)

	// Put 2 batches on disk so pending is non-zero.
	points := []core.DataPoint{{Driver: "plc", Tag: "t", Value: 1.0, Timestamp: time.Now()}}
	for i := 0; i < 2; i++ {
		if err := ob.Push(points, "test"); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}

	eng := New().(*CoreCEngine)
	eng.offlineBuffer = ob
	eng.batchers = map[string]*transportBatcher{"t1": b1, "t2": b2}

	pending, drained, pushed := eng.OfflineBufferStats()
	if pushed != 8 {
		t.Errorf("pushed = %d, want 8 (3+5 summed across batchers)", pushed)
	}
	if pending != 2 {
		t.Errorf("pending = %d, want 2 (shared buffer Len, not 2*2)", pending)
	}
	if drained != 0 {
		t.Errorf("drained = %d, want 0 before any drain", drained)
	}

	// Drain the shared buffer once; drained must reflect the replayed
	// batches exactly (not doubled by the two batchers).
	drainedN, err := ob.Drain(func([]core.DataPoint) error { return nil })
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if drainedN != 2 {
		t.Fatalf("drainedN = %d, want 2", drainedN)
	}

	pending2, drained2, pushed2 := eng.OfflineBufferStats()
	if pushed2 != 8 {
		t.Errorf("pushed = %d, want 8 (unchanged by drain)", pushed2)
	}
	if pending2 != 0 {
		t.Errorf("pending = %d, want 0 after drain", pending2)
	}
	if drained2 != 2 {
		t.Errorf("drained = %d, want 2 (not doubled across batchers)", drained2)
	}
}

// TestEngineOfflineBufferStatsNoBuffer verifies the engine reports zeros
// when no offline buffer and no batchers are configured.
func TestEngineOfflineBufferStatsNoBuffer(t *testing.T) {
	eng := New().(*CoreCEngine)
	pending, drained, pushed := eng.OfflineBufferStats()
	if pending != 0 || drained != 0 || pushed != 0 {
		t.Fatalf("OfflineBufferStats = (%d, %d, %d), want (0, 0, 0) with no buffer", pending, drained, pushed)
	}
}
