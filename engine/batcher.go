package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// Batcher retry defaults. These are internal constants; the retry count
// itself is configurable via TransportConfig.RetryCount.
const (
	defaultRetryBaseDelay = 100 * time.Millisecond
	defaultRetryMaxDelay  = 5 * time.Second
	defaultPublishTimeout = 10 * time.Second

	// defaultDrainInterval is how often the offline-buffer drain loop
	// attempts to replay buffered batches.
	defaultDrainInterval = 30 * time.Second
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

	mu      sync.Mutex
	buffer  []core.DataPoint
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup // tracks the flushLoop + drainLoop goroutines
}

// newTransportBatcher creates a batcher if batching config is set, otherwise returns nil.
// If offlineBuffer is non-nil, the batcher will persist failed batches
// to disk and replay them via a background drain goroutine.
func newTransportBatcher(t core.Transport, cfg core.TransportConfig, offlineBuffer *OfflineBuffer) *transportBatcher {
	if cfg.BatchSize <= 0 && cfg.FlushInterval == "" && cfg.RetryCount <= 0 && offlineBuffer == nil {
		return nil
	}

	b := &transportBatcher{
		transport:     t,
		batchSize:     cfg.BatchSize,
		retryCount:    cfg.RetryCount,
		offlineBuffer: offlineBuffer,
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
		b.flushInterval = 5 * time.Second
	}

	return b
}

func (b *transportBatcher) start(ctx context.Context) {
	b.ctx, b.cancel = context.WithCancel(ctx)
	if b.flushInterval > 0 {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.flushLoop()
		}()
	}
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

func (b *transportBatcher) flushLoop() {
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.flush()
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
		return b.transport.PublishBatch(timeoutCtx, points)
	})
	if err != nil {
		slog.Warn("offline buffer drain error", "transport", b.transport.Name(), "error", err)
	}
	if drained > 0 {
		slog.Info("offline buffer drained", "transport", b.transport.Name(), "batches", drained)
	}
}

// publish adds a point to the buffer and flushes if batch is full.
func (b *transportBatcher) publish(ctx context.Context, point core.DataPoint) error {
	b.mu.Lock()
	b.buffer = append(b.buffer, point)
	shouldFlush := len(b.buffer) >= b.batchSize
	b.mu.Unlock()

	if shouldFlush {
		b.flush()
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

	b.publishWithRetry(b.ctx, batch)
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

	if !b.publishWithRetryAndBuffer(context.Background(), batch) {
		// publishWithRetryAndBuffer already handled the offline buffer.
		return
	}
}

// publishWithRetry sends a batch with exponential backoff. If all
// retries are exhausted and an offline buffer is configured, the
// batch is persisted to disk for later replay.
func (b *transportBatcher) publishWithRetry(ctx context.Context, batch []core.DataPoint) {
	b.publishWithRetryAndBuffer(ctx, batch)
}

// publishWithRetryAndBuffer returns true if the batch was published
// successfully, false if it was buffered to disk (or dropped if no
// offline buffer is configured).
func (b *transportBatcher) publishWithRetryAndBuffer(ctx context.Context, batch []core.DataPoint) bool {
	maxAttempts := b.retryCount + 1
	baseDelay := defaultRetryBaseDelay
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		timeoutCtx, cancel := context.WithTimeout(ctx, defaultPublishTimeout)
		lastErr = b.transport.PublishBatch(timeoutCtx, batch)
		cancel()

		if lastErr == nil {
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
