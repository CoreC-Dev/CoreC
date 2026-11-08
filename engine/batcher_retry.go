package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// publishWithRetryAndBuffer sends a batch with exponential backoff. If all
// retries are exhausted and an offline buffer is configured, the batch is
// persisted to disk for later replay. It is the retry policy's own method
// so transportBatcher stays within its field/method ceilings: the
// attempt count comes from the policy; the transport,
// histograms, offline buffer, and publish callback are reached through
// the owning batcher (r.b).
// Returns true if the batch was published successfully, false if it was
// buffered to disk (or dropped if no offline buffer is configured).
func (r *batcherRetryPolicy) publishWithRetryAndBuffer(ctx context.Context, batch []core.DataPoint) bool {
	b := r.b
	maxAttempts := r.retryCount + 1
	var lastErr error
	pending := batch
	delivered := 0

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Observe data age for each point at the moment of the publish
		// attempt. This captures the true collection-to-publish delay
		// including batching, queuing, and retry backoff (observing at
		// enqueue time instead would miss all of that). Each attempt
		// observes because data age grows during retries, giving
		// operators the real freshness distribution.
		if b.dataAge != nil {
			now := time.Now()
			for i := range pending {
				b.dataAge.Observe(now.Sub(pending[i].Timestamp))
			}
		}

		start := time.Now()
		timeoutCtx, cancel := context.WithTimeout(ctx, defaultPublishTimeout)
		lastErr = b.transport.PublishBatch(timeoutCtx, pending)
		cancel()
		if b.publishLatency != nil {
			b.publishLatency.Observe(time.Since(start))
		}

		if lastErr == nil {
			// Count the real publish (not just the enqueue) so the engine's
			// totalPublish reflects actual successful publishes (counting
			// right after the near-instant enqueue would over-report
			// batches that later fail).
			if b.onPublish != nil {
				b.onPublish(delivered + len(pending))
			}
			return true
		}

		// Partial success (MQTT): retry only the failed indexes; resending
		// the whole batch duplicated every point that already reached the
		// broker.
		next, delta, partial := shrinkToFailed(pending, lastErr)
		if partial {
			delivered += delta
			if len(next) == 0 {
				if b.onPublish != nil {
					b.onPublish(delivered)
				}
				return true
			}
			pending = next
		}

		if attempt < maxAttempts-1 {
			delay := retryDelay(attempt)
			slog.Warn("batch publish retry (only the failed subset is resent when the transport reports per-point failures)",
				"transport", b.transport.Name(),
				"attempt", attempt+1,
				"max", maxAttempts,
				"delay", delay,
				"batch_size", len(pending),
				"error", lastErr,
			)
			select {
			case <-ctx.Done():
				// Context cancelled (e.g. shutdown). Stop retrying and fall
				// through to the buffering logic so the batch is persisted
				// to disk instead of being dropped.
				lastErr = fmt.Errorf("context cancelled: %w", ctx.Err())
				goto exhausted
			case <-time.After(delay):
			}
		}
	}

exhausted:
	// All retries exhausted (or context cancelled). Points already
	// delivered on a partial attempt still count as publishes.
	r.bufferFailed(pending, delivered, lastErr)
	return false
}

// shrinkToFailed interprets a partial-batch error: it returns the points
// that still need a retry, how many points the attempt delivered, and
// whether err was a partial-batch error at all. When everything was
// delivered the returned slice is empty (with partial=true).
func shrinkToFailed(pending []core.DataPoint, err error) (next []core.DataPoint, delivered int, partial bool) {
	var batchErr *core.BatchPublishError
	if !errors.As(err, &batchErr) || len(batchErr.Failed) == 0 {
		return pending, 0, false
	}
	delivered = len(pending) - len(batchErr.Failed)
	next = make([]core.DataPoint, 0, len(batchErr.Failed))
	for _, idx := range batchErr.Failed {
		if idx >= 0 && idx < len(pending) {
			next = append(next, pending[idx])
		}
	}
	return next, delivered, true
}

// retryDelay returns the exponential backoff for a zero-based attempt,
// capped at defaultRetryMaxDelay.
func retryDelay(attempt int) time.Duration {
	delay := defaultRetryBaseDelay * time.Duration(1<<attempt)
	if delay > defaultRetryMaxDelay {
		delay = defaultRetryMaxDelay
	}
	return delay
}

// bufferFailed is the terminal path after retries are exhausted (or the
// context was cancelled): count the points that were delivered on partial
// attempts, then persist what is left to the offline buffer or give up.
func (r *batcherRetryPolicy) bufferFailed(pending []core.DataPoint, delivered int, lastErr error) {
	b := r.b
	if b.onPublish != nil && delivered > 0 {
		b.onPublish(delivered)
	}
	if b.offlineBuffer != nil {
		if bufErr := b.offlineBuffer.Push(pending, b.transport.Name()); bufErr != nil {
			slog.Error("batch publish failed and offline buffer push failed",
				"transport", b.transport.Name(),
				"batch_size", len(pending),
				"publish_error", lastErr,
				"buffer_error", bufErr,
			)
		} else {
			// Count the persisted batch for observability (Prometheus
			// counter corec_offline_buffer_pushed_total). Only successful
			// pushes are counted — a failed Push left nothing on disk.
			r.offlineBufferPushes.Add(1)
			slog.Warn("batch publish failed, buffered to offline storage",
				"transport", b.transport.Name(),
				"batch_size", len(pending),
				"error", lastErr,
			)
		}
	} else {
		slog.Error("batch publish failed after retries",
			"transport", b.transport.Name(),
			"batch_size", len(pending),
			"error", lastErr,
		)
	}
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
	return b.retry.offlineBufferPushes.Load()
}
