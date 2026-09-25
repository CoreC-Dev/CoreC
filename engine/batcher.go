package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/metrics"
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

// transportBatcher wraps a Transport to provide batch buffering,
// periodic flush, and retry with exponential backoff. When an
// OfflineBuffer is configured, batches that exhaust all retries are
// persisted to disk and replayed by a background drain goroutine
// once the transport recovers.
type transportBatcher struct {
	transport     core.Transport
	batchSize     int
	flushInterval time.Duration
	retryCount    int

	// offlineBuffer stores failed batches for later replay. nil = disabled.
	offlineBuffer *OfflineBuffer

	// publishLatency records the duration of each PublishBatch attempt
	// (including retries). Injected from the engine so the batcher's
	// async flush path can observe the real transport latency, not just
	// the in-memory enqueue time (IMPROVEMENTS #1 regression fix: after
	// making publish() async, the observation must move to the async path
	// where Transport.PublishBatch actually runs). nil = no observation
	// (used only in tests that don't check histograms).
	publishLatency *metrics.LatencyHistogram

	// dataAge records the freshness of each data point at publish time.
	// Injected from the engine so the batcher's async flush path can
	// observe the real data age (collection-to-publish delay including
	// batching/retry/offline-replay), not just the enqueue-time age.
	// nil = no observation.
	dataAge *metrics.LatencyHistogram

	// onPublish is called with the number of points actually published
	// when publishWithRetryAndBuffer or drainOnce succeeds. It lets the
	// engine count real publishes (not just enqueues) on the async flush
	// path (Finding 4 fix: previously publish.go counted totalPublish/
	// PushPublish immediately after the near-instant enqueue, which
	// inflated the publish counter and undercounted errors when batches
	// later failed). nil = no callback (tests that don't check counters).
	onPublish func(int)

	// offlineBufferPushes counts the number of batches this batcher has
	// persisted to the offline buffer (after retries were exhausted). It
	// is per-batcher because each transport's publish path pushes
	// independently; the engine aggregates the per-batcher values via
	// OfflineBufferStats. Atomic so it can be read lock-free.
	offlineBufferPushes atomic.Uint64

	// flushBatches is a bounded channel carrying full batches dequeued by
	// publish() for asynchronous flush. publish() never calls flush()
	// synchronously: when the buffer fills it moves the batch here and
	// signals flushLoop, so the processingLoop worker is never blocked by
	// a slow PublishBatch (IMPROVEMENTS #1 option B: the buffer-full
	// flush used to run on the caller's goroutine). Capacity is bounded;
	// when the consumer falls behind, the oldest pending batch is
	// evicted and counted in flushBatchesDropped to bound memory.
	flushBatches        chan []core.DataPoint
	flushBatchesDropped atomic.Uint64

	mu     sync.Mutex
	buffer []core.DataPoint
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // tracks the flushLoop + drainLoop goroutines
}

// newTransportBatcher creates a batcher if batching config is set, otherwise returns nil.
// If offlineBuffer is non-nil, the batcher will persist failed batches
// to disk and replay them via a background drain goroutine.
// publishLatency and dataAge are the engine's histograms for observing
// real transport publish latency and data age on the async flush path.
// They may be nil (tests that don't check histograms).
// onPublish is called with the point count when a batch is actually
// published (not just enqueued). It may be nil (tests).
func newTransportBatcher(t core.Transport, cfg core.TransportConfig, offlineBuffer *OfflineBuffer, publishLatency, dataAge *metrics.LatencyHistogram, onPublish func(int)) *transportBatcher {
	if cfg.BatchSize <= 0 && cfg.FlushInterval == "" && cfg.RetryCount <= 0 && offlineBuffer == nil {
		return nil
	}

	b := &transportBatcher{
		transport:      t,
		batchSize:      cfg.BatchSize,
		retryCount:     cfg.RetryCount,
		offlineBuffer:  offlineBuffer,
		publishLatency: publishLatency,
		dataAge:        dataAge,
		onPublish:      onPublish,
	}

	if cfg.FlushInterval != "" {
		d, err := time.ParseDuration(cfg.FlushInterval)
		if err == nil && d > 0 {
			b.flushInterval = d
		}
	}
	if b.batchSize <= 0 {
		b.batchSize = core.DefaultBatchSize
	}

	// If an offline buffer is configured but no flush interval is set,
	// default to a periodic flush so buffered points are actually sent.
	// Without this, points would accumulate in the buffer indefinitely.
	if b.offlineBuffer != nil && b.flushInterval <= 0 {
		b.flushInterval = defaultFlushInterval
	}

	return b
}

func (b *transportBatcher) start(ctx context.Context) {
	b.ctx, b.cancel = context.WithCancel(ctx)
	// flushBatches carries full batches dequeued by publish() to the
	// flushLoop. Bounded so a slow transport cannot grow memory without
	// limit; evictions are counted for observability.
	b.flushBatches = make(chan []core.DataPoint, defaultFlushBatchQueueSize)
	// Always start the flushLoop so buffer-full batches are flushed
	// asynchronously, even when no periodic flush interval is configured.
	// This keeps publish() non-blocking on the caller's goroutine
	// (IMPROVEMENTS #1 option B).
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
	// Stop the periodic flush/drain loops first and wait for them to
	// exit so they don't race with the final flush.
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
	if b.flushInterval > 0 {
		ticker = time.NewTicker(b.flushInterval)
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
		case batch := <-b.flushBatches:
			b.publishWithRetryAndBuffer(b.ctx, batch)
		case <-tickerC:
			// Periodic flush of any partially-filled buffer.
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
		case batch := <-b.flushBatches:
			b.publishWithRetryAndBuffer(b.ctx, batch)
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
	drained, err := b.offlineBuffer.Drain(func(points []core.DataPoint) error {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), defaultPublishTimeout)
		defer cancel()
		err := b.transport.PublishBatch(timeoutCtx, points)
		if err == nil && b.onPublish != nil {
			// Count replayed publishes so totalPublish reflects all
			// successful publishes, including offline-buffer replays
			// (Finding 4 fix).
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
// even when a slow transport makes PublishBatch take seconds (IMPROVEMENTS
// #1 option B: previously the buffer-full flush ran synchronously on the
// caller's goroutine).
//
// Returns nil when the point was successfully buffered or enqueued for
// async flush. Returns an error only when the flush queue is full and the
// batch containing the caller's point could not be enqueued even after
// evicting the oldest pending batch — in that case the data is lost and the
// caller can try a fallback transport (Finding 4 fix: previously publish()
// always returned nil, making the error/fallback branch in publish.go dead
// code).
func (b *transportBatcher) publish(ctx context.Context, point core.DataPoint) error {
	var fullBatch []core.DataPoint
	b.mu.Lock()
	b.buffer = append(b.buffer, point)
	if len(b.buffer) >= b.batchSize {
		fullBatch = b.buffer
		b.buffer = make([]core.DataPoint, 0, b.batchSize)
	}
	b.mu.Unlock()

	if fullBatch != nil {
		// Non-blocking enqueue. If the queue is full (consumer behind a
		// sustained burst), evict the oldest pending batch to bound memory
		// and count the drop for observability. The evicted batch is
		// logged; with an offline buffer configured the operator can still
		// see pushed counts, but these in-flight evictions are distinct
		// from retry-exhausted pushes.
		select {
		case b.flushBatches <- fullBatch:
			// enqueued successfully
		default:
			// queue full; try to evict oldest to make room
			select {
			case evicted := <-b.flushBatches:
				b.flushBatchesDropped.Add(1)
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
			case b.flushBatches <- fullBatch:
				// enqueued after eviction; the evicted batch was lost but
				// the caller's batch will be published by flushLoop
			default:
				b.flushBatchesDropped.Add(1)
				slog.Warn("batcher flush queue still full, dropping new batch",
					"transport", b.transport.Name(),
					"batch_size", len(fullBatch),
				)
				return fmt.Errorf("batcher flush queue full for transport %s: batch of %d points dropped", b.transport.Name(), len(fullBatch))
			}
		}
	}
	return nil
}

// flush sends all buffered points via PublishBatch with retry.
func (b *transportBatcher) flush() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.buffer
	b.buffer = make([]core.DataPoint, 0, b.batchSize)
	b.mu.Unlock()

	b.publishWithRetryAndBuffer(b.ctx, batch)
}

// flushFinal is the shutdown flush. It uses a fresh background context
// because b.ctx has been cancelled by stop(). This ensures buffered data
// is actually published instead of being silently dropped. If the
// publish fails and an offline buffer is configured, the batch is
// persisted for replay after restart.
func (b *transportBatcher) flushFinal() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.buffer
	b.buffer = make([]core.DataPoint, 0, b.batchSize)
	b.mu.Unlock()

	// Result is ignored: on success the batch is published; on failure
	// publishWithRetryAndBuffer already persisted it to the offline
	// buffer (or logged the drop when no buffer is configured).
	b.publishWithRetryAndBuffer(context.Background(), batch)
}

// publishWithRetryAndBuffer sends a batch with exponential backoff. If all
// retries are exhausted and an offline buffer is configured, the batch is
// persisted to disk for later replay.
// Returns true if the batch was published successfully, false if it was
// buffered to disk (or dropped if no offline buffer is configured).
func (b *transportBatcher) publishWithRetryAndBuffer(ctx context.Context, batch []core.DataPoint) bool {
	maxAttempts := b.retryCount + 1
	baseDelay := defaultRetryBaseDelay
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Observe data age for each point at the moment of the publish
		// attempt. This captures the true collection-to-publish delay
		// including batching, queuing, and retry backoff (IMPROVEMENTS
		// #1 regression fix: observations moved here from publish.go's
		// batcher path, which now returns near-instantly after the async
		// change). Each attempt observes because data age grows during
		// retries, giving operators the real freshness distribution.
		if b.dataAge != nil {
			now := time.Now()
			for i := range batch {
				b.dataAge.Observe(now.Sub(batch[i].Timestamp))
			}
		}

		start := time.Now()
		timeoutCtx, cancel := context.WithTimeout(ctx, defaultPublishTimeout)
		lastErr = b.transport.PublishBatch(timeoutCtx, batch)
		cancel()
		if b.publishLatency != nil {
			b.publishLatency.Observe(time.Since(start))
		}

		if lastErr == nil {
			// Count the real publish (not just the enqueue) so the
			// engine's totalPublish and statManager reflect actual
			// successful publishes (Finding 4 fix: previously counted
			// prematurely in publish.go right after the near-instant
			// enqueue).
			if b.onPublish != nil {
				b.onPublish(len(batch))
			}
			return true
		}

		if attempt < maxAttempts-1 {
			delay := baseDelay * time.Duration(1<<attempt) // exponential backoff
			if delay > defaultRetryMaxDelay {
				delay = defaultRetryMaxDelay
			}
			slog.Warn("batch publish retry (note: partial success may cause duplicates on retry; use QoS≥1 or downstream dedup by driver+tag+timestamp)",
				"transport", b.transport.Name(),
				"attempt", attempt+1,
				"max", maxAttempts,
				"delay", delay,
				"batch_size", len(batch),
				"error", lastErr,
			)
			select {
			case <-ctx.Done():
				// Context cancelled (e.g. shutdown). Stop retrying and
				// fall through to the buffering logic below so the
				// batch is persisted to disk instead of being dropped.
				lastErr = fmt.Errorf("context cancelled: %w", ctx.Err())
				goto exhausted
			case <-time.After(delay):
			}
		}
	}

exhausted:
	// All retries exhausted (or context cancelled).
	if b.offlineBuffer != nil {
		if bufErr := b.offlineBuffer.Push(batch, b.transport.Name()); bufErr != nil {
			slog.Error("batch publish failed and offline buffer push failed",
				"transport", b.transport.Name(),
				"batch_size", len(batch),
				"publish_error", lastErr,
				"buffer_error", bufErr,
			)
		} else {
			// Count the persisted batch for observability (Prometheus
			// counter corec_offline_buffer_pushed_total). Only successful
			// pushes are counted — a failed Push left nothing on disk.
			b.offlineBufferPushes.Add(1)
			slog.Warn("batch publish failed, buffered to offline storage",
				"transport", b.transport.Name(),
				"batch_size", len(batch),
				"error", lastErr,
			)
		}
	} else {
		slog.Error("batch publish failed after retries",
			"transport", b.transport.Name(),
			"batch_size", len(batch),
			"error", lastErr,
		)
	}
	return false
}

// OfflineBufferPending returns the number of batches currently held in this
// batcher's offline buffer awaiting replay. It returns 0 when no offline
// buffer is configured. The value is a point-in-time gauge suitable for
// Prometheus exposure (corec_offline_buffer_pending). It delegates to the
// shared OfflineBuffer, which takes its own lock, so it is safe for
// concurrent use.
func (b *transportBatcher) OfflineBufferPending() int {
	if b.offlineBuffer == nil {
		return 0
	}
	return b.offlineBuffer.Len()
}

// OfflineBufferDrained returns the total number of batches this batcher's
// offline buffer has successfully replayed since startup. It returns 0 when
// no offline buffer is configured. The value is a monotonically increasing
// counter suitable for Prometheus exposure (corec_offline_buffer_drained_total).
func (b *transportBatcher) OfflineBufferDrained() uint64 {
	if b.offlineBuffer == nil {
		return 0
	}
	return b.offlineBuffer.DrainCount()
}

// OfflineBufferPushed returns the total number of batches this batcher has
// persisted to the offline buffer after retries were exhausted. It is a
// per-batcher counter; the engine sums these across batchers via
// OfflineBufferStats for Prometheus exposure (corec_offline_buffer_pushed_total).
func (b *transportBatcher) OfflineBufferPushed() uint64 {
	return b.offlineBufferPushes.Load()
}
