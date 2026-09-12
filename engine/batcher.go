package engine

import (
	"context"
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
)

// transportBatcher wraps a Transport to provide batch buffering,
// periodic flush, and retry with exponential backoff.
type transportBatcher struct {
	transport     core.Transport
	batchSize     int
	flushInterval time.Duration
	retryCount    int

	mu      sync.Mutex
	buffer  []core.DataPoint
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup // tracks the flushLoop goroutine
}

// newTransportBatcher creates a batcher if batching config is set, otherwise returns nil.
func newTransportBatcher(t core.Transport, cfg core.TransportConfig) *transportBatcher {
	if cfg.BatchSize <= 0 && cfg.FlushInterval == "" && cfg.RetryCount <= 0 {
		return nil
	}

	b := &transportBatcher{
		transport:  t,
		batchSize:  cfg.BatchSize,
		retryCount: cfg.RetryCount,
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
}

func (b *transportBatcher) stop() {
	// Stop the periodic flush loop first and wait for it to exit so it
	// doesn't race with the final flush.
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
// is actually published instead of being silently dropped.
func (b *transportBatcher) flushFinal() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.buffer
	b.buffer = make([]core.DataPoint, 0, b.batchSize)
	b.mu.Unlock()

	b.publishWithRetry(context.Background(), batch)
}

func (b *transportBatcher) publishWithRetry(ctx context.Context, batch []core.DataPoint) {
	maxAttempts := b.retryCount + 1
	baseDelay := defaultRetryBaseDelay

	for attempt := 0; attempt < maxAttempts; attempt++ {
		timeoutCtx, cancel := context.WithTimeout(ctx, defaultPublishTimeout)
		err := b.transport.PublishBatch(timeoutCtx, batch)
		cancel()

		if err == nil {
			return
		}

		if attempt < maxAttempts-1 {
			delay := baseDelay * time.Duration(1<<attempt) // exponential backoff
			if delay > defaultRetryMaxDelay {
				delay = defaultRetryMaxDelay
			}
			slog.Warn("batch publish retry",
				"transport", b.transport.Name(),
				"attempt", attempt+1,
				"max", maxAttempts,
				"delay", delay,
				"error", err,
			)
			time.Sleep(delay)
		} else {
			slog.Error("batch publish failed after retries",
				"transport", b.transport.Name(),
				"attempts", maxAttempts,
				"batch_size", len(batch),
				"error", err,
			)
		}
	}
}
