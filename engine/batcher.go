package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// Internal retry/backoff and flush/drain timing defaults. The retry
// counts themselves are configurable via TransportConfig.RetryCount
// (batch publish) and CoreCEngine.writeRetryCount (command write).
const (
	defaultRetryBaseDelay = 100 * time.Millisecond
	defaultRetryMaxDelay  = 5 * time.Second
	defaultPublishTimeout = 10 * time.Second

	// defaultDrainInterval is how often the offline-buffer drain loop
	// attempts to replay buffered batches.
	defaultDrainInterval = 30 * time.Second

	// defaultFlushInterval is the periodic flush interval used when an
	// offline buffer is configured but no explicit FlushInterval is set,
	// so buffered points are actually sent rather than accumulating
	// indefinitely.
	defaultFlushInterval = 5 * time.Second

	// defaultCommandRetryMaxDelay caps the exponential backoff for
	// command-write retries (CoreCEngine.executeWriteWithRetry). It is
	// intentionally tighter than defaultRetryMaxDelay (used for batch
	// publish) because command writes are user-initiated and should fail
	// fast to the dead letter queue instead of blocking the command loop.
	defaultCommandRetryMaxDelay = 2 * time.Second

	// defaultFlushBatchQueueSize bounds the number of full batches pending
	// asynchronous flush per transport. When the consumer (flushLoop)
	// falls behind a sustained burst, the oldest pending batch is evicted
	// to bound memory; the eviction is counted (flushBatchesDropped) so
	// the operator can detect sustained publish backpressure.
	defaultFlushBatchQueueSize = 64
)

// batcherRetryPolicy groups the retry/backoff tuning of the batch publish
// path and its offline-buffer push counter, so transportBatcher itself
// stays under the per-struct field ceiling.
type batcherRetryPolicy struct {
	// retryCount is the number of publish retries after the first
	// attempt (TransportConfig.RetryCount).
	retryCount int

	// shutdownBudget bounds flushFinal, the shutdown flush: the fresh
	// context it publishes under carries this timeout, so a slow retry
	// chain cannot outlive shutdown and abandon the batch mid-retry
	// when the engine stops waiting (or main exits). When the budget
	// expires, publishWithRetryAndBuffer stops retrying and persists
	// the remaining batch to the offline buffer instead. Threading: set
	// from the engine's shutdownTimeout
	// by newTransportBatcher; <= 0 defaults to core.DefaultShutdownTimeout.
	shutdownBudget time.Duration

	// offlineBufferPushes counts the number of batches this batcher has
	// persisted to the offline buffer (after retries were exhausted). It
	// is per-batcher because each transport's publish path pushes
	// independently; the engine aggregates the per-batcher values via
	// OfflineBufferStats. Atomic so it can be read lock-free.
	offlineBufferPushes atomic.Uint64

	// b is the owning batcher: publishWithRetryAndBuffer reaches the
	// transport, histograms, offline buffer, and onPublish callback
	// through it.
	b *transportBatcher
}

// batcherBufferState groups the batcher's mutable in-memory buffering
// state — the partial buffer, the bounded flush queue, and their
// observability counters — so transportBatcher itself stays under the
// per-struct field ceiling. mu guards buffer, stopped, and
// flushBatches together: publish()'s append+enqueue and stop()'s
// stopped-flag must form one critical section.
type batcherBufferState struct {
	batchSize     int
	flushInterval time.Duration

	// flushBatches is a bounded channel carrying full batches dequeued by
	// publish() for asynchronous flush. publish() never calls flush()
	// synchronously: when the buffer fills it moves the batch here and
	// signals flushLoop, so the processingLoop worker is never blocked by
	// a slow PublishBatch (a synchronous buffer-full flush would block
	// the caller's goroutine). Capacity is bounded;
	// when the consumer falls behind, the oldest pending batch is
	// evicted and counted in flushBatchesDropped to bound memory.
	flushBatches        chan []core.DataPoint
	flushBatchesDropped atomic.Uint64

	// stopped is set as the first step of stop() (under mu, before the
	// loops are cancelled). Once set, publish() rejects new points: after
	// flushFinal has swapped the buffer out, a late append would strand
	// the point in memory forever, and a late enqueue into flushBatches
	// would strand a whole batch in the channel — both silently. Atomic
	// because it is paired
	// with publishRejected for lock-free observability reads.
	stopped atomic.Bool

	// publishRejected counts the points publish() rejected because the
	// batcher had already been stopped. The rejection converts a silent
	// shutdown-time loss into a counted, logged drop. Atomic so it can
	// be read lock-free.
	publishRejected atomic.Uint64

	mu     sync.Mutex
	buffer []core.DataPoint
}

// transportBatcher wraps a Transport to provide batch buffering,
// periodic flush, and retry with exponential backoff. When an
// OfflineBuffer is configured, batches that exhaust all retries are
// persisted to disk and replayed by a background drain goroutine
// once the transport recovers. Its state is split into the retry
// policy (batcherRetryPolicy) and the buffering state
// (batcherBufferState) sub-structs to keep each under the per-struct
// field ceiling.
type transportBatcher struct {
	transport core.Transport

	// offlineBuffer stores failed batches for later replay. nil = disabled.
	offlineBuffer *OfflineBuffer

	// publishLatency records the duration of each PublishBatch attempt
	// (including retries). Injected from the engine as a latencyObserver:
	// normally a fan-out adapter that feeds BOTH the global aggregate
	// histogram and the per-transport labeled histogram,
	// but a plain metrics.LatencyHistogram (or nil in tests) also
	// satisfies the interface.
	publishLatency latencyObserver

	// dataAge records the freshness of each data point at publish time,
	// same observer contract as publishLatency: global + per-transport
	// labeled histogram via the engine's fan-out adapter.
	dataAge latencyObserver

	// onPublish is called with the number of points actually published
	// when publishWithRetryAndBuffer or drainOnce succeeds. It lets the
	// engine count real publishes (not just enqueues) on the async flush
	// path: counting at enqueue time would inflate the publish counter
	// and undercount errors when batches later fail.
	// nil = no callback (tests that don't check counters).
	onPublish func(int)

	// retry holds the retry/backoff tuning and the offline-buffer
	// push counter.
	retry batcherRetryPolicy

	// buf holds the mutable buffering state: partial buffer, flush
	// queue, and their counters.
	buf batcherBufferState

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // tracks the flushLoop + drainLoop goroutines
}

// newTransportBatcher creates a batcher if batching config is set, otherwise returns nil.
// If offlineBuffer is non-nil, the batcher will persist failed batches
// to disk and replay them via a background drain goroutine.
// publishLatency and dataAge are latencyObservers for the real
// transport publish latency and data age on the async flush path — the
// engine passes fan-out adapters (global aggregate + per-transport
// labeled histogram). They may be nil (tests that don't
// check histograms).
// onPublish is called with the point count when a batch is actually
// published (not just enqueued). It may be nil (tests).
// shutdownBudget bounds the shutdown flush (flushFinal); pass the
// engine's shutdownTimeout so the final flush cannot outlive the
// engine's own stop timeout. <= 0 defaults to
// core.DefaultShutdownTimeout.
func newTransportBatcher(t core.Transport, cfg core.TransportConfig, offlineBuffer *OfflineBuffer, publishLatency, dataAge latencyObserver, onPublish func(int), shutdownBudget time.Duration) *transportBatcher {
	if cfg.BatchSize <= 0 && cfg.FlushInterval == "" && cfg.RetryCount <= 0 && offlineBuffer == nil {
		return nil
	}

	b := &transportBatcher{
		transport:      t,
		offlineBuffer:  offlineBuffer,
		publishLatency: publishLatency,
		dataAge:        dataAge,
		onPublish:      onPublish,
	}
	// Wire the retry policy's back-pointer so its method can reach the
	// transport, histograms, and callbacks through the owning batcher.
	b.retry.b = b
	b.retry.retryCount = cfg.RetryCount

	if shutdownBudget > 0 {
		b.retry.shutdownBudget = shutdownBudget
	} else {
		b.retry.shutdownBudget = core.DefaultShutdownTimeout
	}

	if cfg.FlushInterval != "" {
		d, err := time.ParseDuration(cfg.FlushInterval)
		if err == nil && d > 0 {
			b.buf.flushInterval = d
		}
	}
	b.buf.batchSize = cfg.BatchSize
	if b.buf.batchSize <= 0 {
		b.buf.batchSize = core.DefaultBatchSize
	}

	// If an offline buffer is configured but no flush interval is set,
	// default to a periodic flush so buffered points are actually sent.
	// Without this, points would accumulate in the buffer indefinitely.
	if b.offlineBuffer != nil && b.buf.flushInterval <= 0 {
		b.buf.flushInterval = defaultFlushInterval
	}

	return b
}

func (b *transportBatcher) start(ctx context.Context) {
	b.ctx, b.cancel = context.WithCancel(ctx)
	// flushBatches carries full batches dequeued by publish() to the
	// flushLoop. Bounded so a slow transport cannot grow memory without
	// limit; evictions are counted for observability.
	b.buf.flushBatches = make(chan []core.DataPoint, defaultFlushBatchQueueSize)
	// Always start the flushLoop so buffer-full batches are flushed
	// asynchronously, even when no periodic flush interval is configured.
	// This keeps publish() non-blocking on the caller's goroutine.
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.flushLoop()
	}()
	// Start the offline-buffer drain loop if an offline buffer is
	// configured. This periodically replays buffered batches.
	if b.offlineBuffer != nil {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.drainLoop()
		}()
	}
}

func (b *transportBatcher) stop() {
	// Mark the batcher stopped BEFORE cancelling the loops so
	// concurrent publish() calls cannot strand data after the final
	// flush. The flag is checked
	// under b.buf.mu inside publish(), and publish() completes its
	// append+enqueue within the same critical section, so every point
	// accepted before this line is covered by the flushes below and
	// every point after it is rejected, counted and logged.
	b.buf.mu.Lock()
	b.buf.stopped.Store(true)
	b.buf.mu.Unlock()

	// Stop the periodic flush/drain loops first and wait for them to
	// exit so they don't race with the final flush. On shutdown the
	// flushLoop drains every batch still queued in flushBatches before
	// returning, so nothing enqueued before the stopped flag was set can
	// be left behind.
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()

	// Final flush — use a fresh context because b.ctx is now cancelled.
	// Without this, buffered data would be silently dropped on shutdown
	// since PublishBatch would receive an already-cancelled context.
	b.flushFinal()
}

// flushLoop consumes full batches from flushBatches (enqueued by publish()
// when the buffer fills) and periodically flushes any partial buffer when
// a flush interval is configured. All flushes run on this goroutine so the
// publishing worker is never blocked by PublishBatch latency. On shutdown
// (ctx cancelled) it drains any remaining pending batches before exiting so
// no in-flight batch is silently lost.
func (b *transportBatcher) flushLoop() {
	var ticker *time.Ticker
	var tickerC <-chan time.Time
	if b.buf.flushInterval > 0 {
		ticker = time.NewTicker(b.buf.flushInterval)
		defer ticker.Stop()
		tickerC = ticker.C
	}
	for {
		select {
		case <-b.ctx.Done():
			// Drain any remaining pending batches so shutdown does not
			// lose in-flight data. publishWithRetryAndBuffer will persist
			// failed batches to the offline buffer when configured.
			b.drainFlushBatches()
			return
		case batch := <-b.buf.flushBatches:
			b.retry.publishWithRetryAndBuffer(b.ctx, batch)
		case <-tickerC:
			// Periodic flush of any partially-filled buffer. Older full
			// batches still queued in flushBatches are published FIRST:
			// this select cannot prioritize the queue over the ticker,
			// and when both are ready the pseudo-random choice could
			// otherwise publish the NEWER partial buffer ahead of older
			// queued batches — cross-batch reordering. flush() additionally
			// defers itself while any
			// backlog remains, closing the window where a batch is
			// enqueued between the drain and the flush.
			b.drainFlushBatches()
			b.flush()
		}
	}
}

// drainFlushBatches processes all batches currently queued in flushBatches.
// Called during shutdown so no enqueued batch is dropped. It uses the
// batcher context (already cancelled) which causes publishWithRetryAndBuffer
// to persist batches to the offline buffer instead of attempting network I/O.
func (b *transportBatcher) drainFlushBatches() {
	for {
		select {
		case batch := <-b.buf.flushBatches:
			b.retry.publishWithRetryAndBuffer(b.ctx, batch)
		default:
			return
		}
	}
}

// drainLoop periodically attempts to replay batches that were buffered
// to disk when the transport was unavailable.
func (b *transportBatcher) drainLoop() {
	ticker := time.NewTicker(defaultDrainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.drainOnce()
		}
	}
}

func (b *transportBatcher) drainOnce() {
	if b.offlineBuffer == nil {
		return
	}
	// Pass this batcher's transport name so Drain only replays batches
	// this transport originally accepted — the buffer is shared across
	// all batchers and replaying a foreign batch through the wrong
	// transport would misroute the data.
	drained, err := b.offlineBuffer.Drain(b.transport.Name(), func(points []core.DataPoint) error {
		// Inherit b.ctx (cancelled by stop()) so an in-flight replay can
		// be aborted promptly during shutdown instead of running to the
		// full per-batch timeout against a transport that may already be
		// gone. The per-batch timeout still
		// bounds each attempt during normal operation.
		baseCtx := b.ctx
		if baseCtx == nil {
			// drainOnce is only wired to drainLoop, which start()
			// launches; tests may invoke it on an unstarted batcher,
			// where no shutdown can race the replay.
			baseCtx = context.Background()
		}
		timeoutCtx, cancel := context.WithTimeout(baseCtx, defaultPublishTimeout)
		defer cancel()
		err := b.transport.PublishBatch(timeoutCtx, points)
		if err == nil && b.onPublish != nil {
			// Count replayed publishes so totalPublish reflects all
			// successful publishes, including offline-buffer replays.
			b.onPublish(len(points))
		}
		return err
	})
	if err != nil {
		slog.Warn("offline buffer drain error", "transport", b.transport.Name(), "error", err)
	}
	if drained > 0 {
		slog.Info("offline buffer drained", "transport", b.transport.Name(), "batches", drained)
	}
}

// publish adds a point to the buffer and, when the batch is full, hands the
// full batch to flushLoop asynchronously. It never blocks the caller: the
// flush (PublishBatch + retry + offline buffering) runs on the batcher's
// own flushLoop goroutine. This keeps the processingLoop worker responsive
// even when a slow transport makes PublishBatch take seconds (a synchronous
// buffer-full flush would block the caller's goroutine for that long).
//
// The append and the flush-queue enqueue happen in one b.buf.mu critical
// section together with the stopped check: stop() sets the stopped flag
// under the same mutex before
// cancelling the loops, so a batch can never be enqueued after flushLoop
// has drained the queue on its way out, and a point can never be appended
// after flushFinal has swapped the buffer. Publishes that lose this race
// are rejected, counted in publishRejected and logged instead of being
// silently stranded. All channel operations below are non-blocking, so
// holding the mutex cannot deadlock against flushLoop.
//
// Returns nil when the point was successfully buffered or enqueued for
// async flush. Returns an error when the batcher is stopped (point
// rejected) or when the flush queue is full and the batch containing the
// caller's point could not be enqueued even after evicting the oldest
// pending batch — in both cases the data is lost and the caller can try
// a fallback transport (returning nil here instead would leave the
// error/fallback branch in the publish path dead code).
func (b *transportBatcher) publish(ctx context.Context, point core.DataPoint) error {
	b.buf.mu.Lock()
	defer b.buf.mu.Unlock()

	if b.buf.stopped.Load() {
		b.buf.publishRejected.Add(1)
		slog.Warn("batcher stopped, rejecting publish (point dropped)",
			"transport", b.transport.Name(),
			"tag", point.Tag,
		)
		return fmt.Errorf("batcher for transport %s already stopped: point dropped", b.transport.Name())
	}

	b.buf.buffer = append(b.buf.buffer, point)
	if len(b.buf.buffer) < b.buf.batchSize {
		return nil
	}
	fullBatch := b.buf.buffer
	b.buf.buffer = make([]core.DataPoint, 0, b.buf.batchSize)

	// Non-blocking enqueue. If the queue is full (consumer behind a
	// sustained burst), evict the oldest pending batch to bound memory
	// and count the drop for observability. The evicted batch is
	// logged; with an offline buffer configured the operator can still
	// see pushed counts, but these in-flight evictions are distinct
	// from retry-exhausted pushes.
	select {
	case b.buf.flushBatches <- fullBatch:
		// enqueued successfully
		return nil
	default:
	}
	// queue full; try to evict oldest to make room
	select {
	case evicted := <-b.buf.flushBatches:
		b.buf.flushBatchesDropped.Add(1)
		slog.Warn("batcher flush queue full, evicting oldest pending batch",
			"transport", b.transport.Name(),
			"evicted_batch_size", len(evicted),
		)
	default:
	}
	// Retry enqueue after eviction. If still full, the new batch
	// (containing the caller's point) is lost — return an error so
	// the caller can try a fallback transport.
	select {
	case b.buf.flushBatches <- fullBatch:
		// enqueued after eviction; the evicted batch was lost but
		// the caller's batch will be published by flushLoop
		return nil
	default:
		b.buf.flushBatchesDropped.Add(1)
		slog.Warn("batcher flush queue still full, dropping new batch",
			"transport", b.transport.Name(),
			"batch_size", len(fullBatch),
		)
		return fmt.Errorf("batcher flush queue full for transport %s: batch of %d points dropped", b.transport.Name(), len(fullBatch))
	}
}

// flush sends all buffered points via PublishBatch with retry. It is
// called by flushLoop on the periodic tick.
//
// While older full batches are still queued in flushBatches the flush
// is deferred (returns without publishing): the points in the partial
// buffer are newer than every queued batch, so sending them now would
// reorder batches across the queue. The
// queue check and the buffer swap happen in one b.buf.mu critical
// section — publish() appends and enqueues under the same mutex — so a
// batch cannot slip in between the check and the swap. The queued
// batches are consumed by flushLoop's main select, and the partial
// buffer goes out on a later tick once the backlog is clear.
func (b *transportBatcher) flush() {
	b.buf.mu.Lock()
	if len(b.buf.buffer) == 0 || len(b.buf.flushBatches) > 0 {
		b.buf.mu.Unlock()
		return
	}
	batch := b.buf.buffer
	b.buf.buffer = make([]core.DataPoint, 0, b.buf.batchSize)
	b.buf.mu.Unlock()

	b.retry.publishWithRetryAndBuffer(b.ctx, batch)
}

// flushFinal is the shutdown flush. It uses a fresh context (b.ctx has
// been cancelled by stop()) so buffered data is actually published
// instead of being silently dropped. The fresh context carries a
// shutdownBudget timeout: without it, a transport with a large
// retry-count and a slow or unreachable broker could keep the final
// flush retrying long after the engine gave up waiting (or after main
// called os.Exit), silently losing the batch. With the budget, when
// the deadline fires the retry loop's
// backoff select observes ctx.Done and falls through to persisting the
// batch to the offline buffer (or logging the drop when no buffer is
// configured) instead of abandoning it mid-retry.
func (b *transportBatcher) flushFinal() {
	b.buf.mu.Lock()
	if len(b.buf.buffer) == 0 {
		b.buf.mu.Unlock()
		return
	}
	batch := b.buf.buffer
	b.buf.buffer = make([]core.DataPoint, 0, b.buf.batchSize)
	b.buf.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), b.retry.shutdownBudget)
	defer cancel()

	// Result is ignored: on success the batch is published; on failure
	// publishWithRetryAndBuffer already persisted it to the offline
	// buffer (or logged the drop when no buffer is configured).
	b.retry.publishWithRetryAndBuffer(ctx, batch)
}
