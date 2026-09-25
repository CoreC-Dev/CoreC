package engine

import (
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/rule"
)

// applyTransform evaluates the transform expression against the data point's
// value and applies the tag rename, returning a new point. If the transform
// config is nil, the expression is empty, or the value is non-numeric, the
// original point is returned unchanged (with a warning logged on eval failure).
func (e *CoreCEngine) applyTransform(point core.DataPoint, tc *core.TransformConfig) core.DataPoint {
	if tc == nil || tc.Expression == "" {
		return point
	}

	raw, ok := numericValue(point.Value)
	if !ok {
		// Only numeric values can be arithmetically transformed.
		return point
	}

	result, err := rule.EvalArith(tc.Expression, raw)
	if err != nil {
		slog.Warn("transform expression eval failed, keeping original value",
			"tag", point.Tag, "expression", tc.Expression, "error", err)
		return point
	}

	out := point
	out.Value = result
	if tc.TagRename != "" {
		out.Tag = tc.TagRename
	}
	return out
}

// numericValue returns the float64 representation of a numeric value and true,
// or 0 and false for non-numeric types (bool, string, nil, etc.).
func numericValue(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int8:
		return float64(val), true
	case int16:
		return float64(val), true
	case int32:
		return float64(val), true
	case int64:
		return float64(val), true
	case uint:
		return float64(val), true
	case uint8:
		return float64(val), true
	case uint16:
		return float64(val), true
	case uint32:
		return float64(val), true
	case uint64:
		return float64(val), true
	}
	return 0, false
}

// publishTargetEntry is a snapshot of a single transport and its optional
// batcher, captured under the engine read lock so that Publish can be
// called outside the lock (problem 8).
type publishTargetEntry struct {
	name      string
	transport core.Transport
	batcher   *transportBatcher // nil if no batcher configured
}

func (e *CoreCEngine) publishToTargets(point core.DataPoint, targets []string) {
	// Snapshot transports and batchers under RLock, then release the
	// lock before calling Publish. This prevents slow network I/O from
	// blocking management operations (AddDriver/RemoveDriver/etc.) that
	// need the write lock (problem 8).
	e.mu.RLock()
	snapshot := make([]publishTargetEntry, 0, len(targets))
	for _, targetName := range targets {
		transport, ok := e.transports[targetName]
		if !ok {
			slog.Warn("target transport not found", "target", targetName)
			e.totalErrors.Add(1)
			continue
		}
		entry := publishTargetEntry{name: targetName, transport: transport}
		if batcher, ok := e.batchers[targetName]; ok {
			entry.batcher = batcher
		}
		snapshot = append(snapshot, entry)
	}

	// Also snapshot fallback transports for failover.
	fallbackSnapshot := make(map[string]publishTargetEntry, len(snapshot))
	for _, entry := range snapshot {
		if cfg, ok := e.transportCfg[entry.name]; ok && cfg.Fallback != "" {
			if ft, ok2 := e.transports[cfg.Fallback]; ok2 {
				fbEntry := publishTargetEntry{name: cfg.Fallback, transport: ft}
				if batcher, ok2 := e.batchers[cfg.Fallback]; ok2 {
					fbEntry.batcher = batcher
				}
				fallbackSnapshot[entry.name] = fbEntry
			}
		}
	}
	e.mu.RUnlock()

	for _, entry := range snapshot {
		// Use batcher if configured, otherwise publish directly
		if entry.batcher != nil {
			// publishLatency and dataAge are observed inside the
			// batcher's publishWithRetryAndBuffer (around the actual
			// PublishBatch call), not here — publish() is now async
			// (IMPROVEMENTS #1) and returns near-instantly, so
			// observing here would record enqueue time, not real
			// transport latency.
			//
			// totalPublish and statManager.PushPublish are also counted
			// inside the batcher via the onPublish callback (called when
			// the batch is actually published, not just enqueued). This
			// avoids inflating the publish counter for points that are
			// later lost after retries are exhausted (Finding 4 fix:
			// previously counted prematurely here).
			//
			// publish() returns an error only when the flush queue is
			// full and the batch is dropped — in that case the data is
			// lost, so we count the error and try the fallback transport.
			err := entry.batcher.publish(e.ctx, point)
			if err != nil {
				slog.Error("batch dropped from flush queue", "target", entry.name, "error", err)
				e.totalErrors.Add(1)
				e.statManager.PushError()
				e.tryFallback(point, entry.name, fallbackSnapshot)
			}
			continue
		}

		start := time.Now()
		err := entry.transport.Publish(e.ctx, point)
		e.publishLatency.Observe(time.Since(start))
		e.dataAge.Observe(time.Since(point.Timestamp))
		if err != nil {
			slog.Error("publish failed", "target", entry.name, "error", err)
			e.totalErrors.Add(1)
			e.statManager.PushError()
			e.tryFallback(point, entry.name, fallbackSnapshot)
		} else {
			e.totalPublish.Add(1)
			e.statManager.PushPublish(1)
		}
	}
}

// tryFallback attempts to publish a point to the fallback transport
// configured for the given primary target name. If no fallback is
// configured or the fallback also fails, the error is logged.
func (e *CoreCEngine) tryFallback(point core.DataPoint, primaryName string, fallbacks map[string]publishTargetEntry) {
	fb, ok := fallbacks[primaryName]
	if !ok {
		return
	}
	slog.Warn("transport failover to fallback", "primary", primaryName, "fallback", fb.name)

	if fb.batcher != nil {
		// Observations and publish counting happen inside the
		// batcher's async flush path (see comment in publishToTargets
		// above). publish() returns an error only when the batch is
		// dropped from the flush queue.
		err := fb.batcher.publish(e.ctx, point)
		if err != nil {
			slog.Error("fallback batch dropped from flush queue", "fallback", fb.name, "error", err)
			e.totalErrors.Add(1)
			e.statManager.PushError()
		}
		return
	}

	start := time.Now()
	err := fb.transport.Publish(e.ctx, point)
	e.publishLatency.Observe(time.Since(start))
	e.dataAge.Observe(time.Since(point.Timestamp))
	if err != nil {
		slog.Error("fallback publish also failed", "fallback", fb.name, "error", err)
		e.totalErrors.Add(1)
		e.statManager.PushError()
	} else {
		e.totalPublish.Add(1)
		e.statManager.PushPublish(1)
	}
}

func (e *CoreCEngine) fireAlert(point core.DataPoint, r core.Rule) {
	e.mu.RLock()
	handlers := make([]func(core.DataPoint, core.Rule), len(e.alertHandlers))
	copy(handlers, e.alertHandlers)
	e.mu.RUnlock()

	for _, h := range handlers {
		h(point, r)
	}
}
