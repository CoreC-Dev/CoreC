package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// transportManager groups the northbound transport concern: the live
// transport map, the transport configs (for fallback lookups), the
// per-transport batchers, the per-transport listener cancellers, and the
// shared offline buffer. Extracted from CoreCEngine in the CPLX-020 God
// Object split; CoreCEngine keeps thin forwarders so the
// core.TransportManager method set is unchanged.
type transportManager struct {
	transports   map[string]core.Transport
	transportCfg map[string]core.TransportConfig // for fallback lookups
	batchers     map[string]*transportBatcher

	// transportCancels holds the per-transport context cancel func for each
	// transport's startCommandListener/startDataListener goroutines. Each
	// listener selects on a child context derived from the engine context
	// instead of the engine context directly, so RemoveTransport can stop
	// just that transport's listeners without cancelling the whole engine.
	// This fixes the goroutine leak where MQTT transports' listener
	// goroutines blocked forever after RemoveTransport (MQTTTransport.Stop
	// deliberately does not close its command/data channels).
	transportCancels map[string]context.CancelFunc

	// offlineBuffer persists failed publish batches to disk for replay
	// after transport recovery. nil = disabled (no buffer config).
	offlineBuffer *OfflineBuffer

	// eng is the owning engine: AddTransport/RemoveTransport reach the
	// shared lock, lifecycle context, stats histograms, and shutdown
	// timeout through it.
	eng *CoreCEngine
}

// --- Transport Management ---

func (m *transportManager) AddTransport(config core.TransportConfig) error {
	transport, err := core.CreateTransport(config)
	if err != nil {
		return fmt.Errorf("failed to create transport %s: %w", config.Name, err)
	}

	if err := transport.Init(m.eng.runtime.ctx, config); err != nil {
		return fmt.Errorf("failed to init transport %s: %w", config.Name, err)
	}

	if err := transport.Start(m.eng.runtime.ctx); err != nil {
		return fmt.Errorf("failed to start transport %s: %w", config.Name, err)
	}

	m.eng.mu.Lock()
	// If a transport with the same name already exists, remove it and its
	// batcher from the map now so concurrent callers don't see a stopping
	// transport. The actual Stop() happens after Unlock to avoid blocking.
	var oldTransport core.Transport
	var oldBatcher *transportBatcher
	var oldCancel context.CancelFunc
	if t, exists := m.transports[config.Name]; exists {
		oldTransport = t
		delete(m.transports, config.Name)
		if b, hasBatcher := m.batchers[config.Name]; hasBatcher {
			oldBatcher = b
			delete(m.batchers, config.Name)
		}
		if c, hasCancel := m.transportCancels[config.Name]; hasCancel {
			oldCancel = c
			delete(m.transportCancels, config.Name)
		}
	}
	m.eng.mu.Unlock()

	// Cancel the old transport's listener goroutines before stopping it so
	// they don't block forever on the (unclosed) MQTT channels.
	if oldCancel != nil {
		oldCancel()
	}
	// Stop the old transport and batcher outside the lock. The batcher
	// stop is bounded by the engine's shutdown timeout (stopBatcherWithTimeout):
	// a batcher stuck in a long flush retry chain must not block the
	// management API indefinitely (stage-B audit findings B3.1/B3.6 —
	// previously this was an unbounded b.stop()).
	if oldBatcher != nil {
		m.stopBatcherWithTimeout(oldBatcher, config.Name, "transport replacement")
	}
	if oldTransport != nil {
		if err := oldTransport.Stop(); err != nil {
			slog.Error("failed to stop old transport on replacement", "name", config.Name, "error", err)
		}
	}

	m.eng.mu.Lock()
	m.transports[config.Name] = transport
	m.transportCfg[config.Name] = config

	// Create batcher if batch/flush/retry config is set, or if offline
	// buffering is enabled (so failed publishes are persisted).
	// The engine's publishLatency and dataAge histograms are injected so
	// the batcher's async flush path can observe real transport publish
	// latency and data age (IMPROVEMENTS #1 regression fix). The
	// onBatchPublish callback is injected so the batcher counts real
	// publishes (not just enqueues) on the async flush path (Finding 4
	// fix: previously publish.go counted prematurely). The engine's
	// shutdownTimeout is threaded through as the batcher's flushFinal
	// budget so the shutdown flush cannot outlive shutdown (B3.1).
	if b := newTransportBatcher(transport, config, m.offlineBuffer,
		m.eng.stats.labeled.publishObserver(transport.Name()),
		m.eng.stats.labeled.ageObserver(transport.Name()),
		m.eng.stats.onBatchPublish, m.eng.runtime.shutdownTimeout); b != nil {
		b.start(m.eng.runtime.ctx)
		m.batchers[config.Name] = b
		slog.Info("transport batching enabled",
			"name", config.Name,
			"batch-size", config.BatchSize,
			"flush-interval", config.FlushInterval,
			"retry-count", config.RetryCount,
		)
	}

	m.eng.mu.Unlock()

	// Start command and data listeners for this transport if the engine is
	// already running.  This fixes M29: transports added after Start() now
	// receive write commands and ingest data.
	//
	// Each transport gets its own child context derived from the engine
	// context so that RemoveTransport can cancel just this transport's
	// listeners without tearing down the whole engine. This fixes the
	// goroutine leak where MQTT listener goroutines blocked forever after
	// RemoveTransport.
	if m.eng.runtime.ctx != nil && m.eng.runtime.ctx.Err() == nil {
		tCtx, tCancel := context.WithCancel(m.eng.runtime.ctx)
		m.eng.mu.Lock()
		m.transportCancels[config.Name] = tCancel
		m.eng.mu.Unlock()
		m.eng.writes.startCommandListener(transport, tCtx)
		m.startDataListener(transport, tCtx)
	}

	slog.Info("transport added", "name", config.Name, "type", config.Type)
	return nil
}

func (m *transportManager) RemoveTransport(name string) error {
	m.eng.mu.Lock()
	transport, ok := m.transports[name]
	if !ok {
		m.eng.mu.Unlock()
		return fmt.Errorf("transport not found: %s", name)
	}
	delete(m.transports, name)
	delete(m.transportCfg, name)

	// Also remove and stop the associated batcher so its flushLoop
	// goroutine doesn't keep running and try to publish to the
	// stopped transport. Without this, RemoveTransport leaks the
	// batcher goroutine and publishToTargets may still route data
	// through the stale batcher (problem 1+2).
	var batcher *transportBatcher
	if b, hasBatcher := m.batchers[name]; hasBatcher {
		batcher = b
		delete(m.batchers, name)
	}

	// Cancel this transport's listener goroutines so they exit instead of
	// blocking forever on the (unclosed) MQTT command/data channels. This
	// fixes the goroutine leak: the engine ctx stays alive (only full Stop
	// cancels it) and MQTTTransport.Stop deliberately does not close its
	// channels, so without per-transport cancellation the
	// startCommandListener/startDataListener goroutines would block forever.
	var tCancel context.CancelFunc
	if c, hasCancel := m.transportCancels[name]; hasCancel {
		tCancel = c
		delete(m.transportCancels, name)
	}
	m.eng.mu.Unlock()

	if tCancel != nil {
		tCancel()
	}

	if batcher != nil {
		// Bounded by the engine's shutdown timeout (stage-B audit
		// findings B3.1/B3.6): RemoveTransport is a management API call
		// and must not hang behind a batcher stuck in a long flush retry
		// chain. On timeout the stop goroutine is abandoned; the
		// batcher's flushFinal runs on its own shutdown budget and
		// terminates by publishing or persisting to the offline buffer.
		m.stopBatcherWithTimeout(batcher, name, "transport removal")
	}
	return transport.Stop()
}

// stopBatcherWithTimeout stops a batcher, waiting at most the engine's
// shutdown timeout. batcher.stop() flushes remaining data and can
// legitimately take seconds (retry chains, offline-buffer persistence),
// but it must never block the caller indefinitely: the transport
// removal/replacement paths run on management API goroutines, and an
// unbounded stop behind a transport with an oversized retry-count could
// hang them (stage-B audit findings B3.1/B3.6). On timeout the stop
// goroutine is abandoned; flushFinal's own budget (shutdownBudget)
// ensures the in-flight flush terminates by publishing the batch or
// persisting it to the offline buffer.
func (m *transportManager) stopBatcherWithTimeout(b *transportBatcher, name, reason string) {
	done := make(chan struct{}, 1)
	go func() {
		b.stop()
		done <- struct{}{}
	}()
	select {
	case <-done:
		slog.Info("batcher stopped", "name", name)
	case <-time.After(m.eng.runtime.shutdownTimeout):
		slog.Error("timed out stopping batcher",
			"name", name,
			"reason", reason,
			"timeout", m.eng.runtime.shutdownTimeout,
		)
	}
}

func (m *transportManager) GetTransport(name string) (core.Transport, bool) {
	m.eng.mu.RLock()
	defer m.eng.mu.RUnlock()
	t, ok := m.transports[name]
	return t, ok
}

func (m *transportManager) ListTransports() []core.TransportStatus {
	m.eng.mu.RLock()
	defer m.eng.mu.RUnlock()
	result := make([]core.TransportStatus, 0, len(m.transports))
	for _, t := range m.transports {
		result = append(result, t.Status())
	}
	return result
}

// startTransports initializes and adds all configured transports.
func (m *transportManager) startTransports(config *core.Config) error {
	for _, tc := range config.Transports {
		if err := m.AddTransport(tc); err != nil {
			slog.Error("failed to add transport", "name", tc.Name, "error", err)
			return fmt.Errorf("failed to add transport %s: %w", tc.Name, err)
		}
	}
	return nil
}

// startDataListener launches a goroutine that listens for data points from
// a transport's OnData channel and feeds them into the DataBus — the
// chained-core inbound path.  DataPoints are piped back to the
// processing loop via a channel so chained transports feed the same
// pipeline as driver-sourced data.
//
// It is called from AddTransport for every transport (initial and runtime).
func (m *transportManager) startDataListener(t core.Transport, tCtx context.Context) {
	dataCh := t.OnData()
	if dataCh == nil {
		return
	}
	m.eng.runtime.wg.Add(1)
	go func(ch <-chan core.DataPoint, transportName string) {
		defer m.eng.runtime.wg.Done()
		slog.Info("data listener started", "transport", transportName)
		for {
			select {
			case <-tCtx.Done():
				return
			case point, ok := <-ch:
				if !ok {
					return
				}
				m.eng.stats.recordRead(1)
				m.eng.processing.dataBus.Push(point)
			}
		}
	}(dataCh, t.Name())
}

// tryFallback attempts to publish a point to the fallback transport
// configured for the given primary target name. If no fallback is
// configured or the fallback also fails, the error is logged.
func (m *transportManager) tryFallback(point core.DataPoint, primaryName string, fallbacks map[string]publishTargetEntry) {
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
		err := fb.batcher.publish(m.eng.runtime.ctx, point)
		if err != nil {
			slog.Error("fallback batch dropped from flush queue", "fallback", fb.name, "error", err)
			m.eng.stats.totalErrors.Add(1)
					}
		return
	}

	start := time.Now()
	err := fb.transport.Publish(m.eng.runtime.ctx, point)
	m.eng.stats.labeled.observePublish(fb.name, time.Since(start))
	m.eng.stats.labeled.observeAge(fb.name, time.Since(point.Timestamp))
	if err != nil {
		slog.Error("fallback publish also failed", "fallback", fb.name, "error", err)
		m.eng.stats.totalErrors.Add(1)
			} else {
		m.eng.stats.totalPublish.Add(1)
	}
}

// ingressDeadLetters aggregates the transport-local dead-letter stores:
// commands dropped at transport ingress (e.g. an MQTT command arriving
// while the command channel was full), which previously had only a
// counter (corec_transport_dropped_commands_total) with no queryable
// detail (audit findings RES-1/TRA-4). Transports opt in by implementing
// core.IngressDeadLetterSource; the entry's Transport field records
// which transport dropped it. Returns nil when no transport reports
// ingress drops.
func (m *transportManager) ingressDeadLetters() []core.DeadLetterEntry {
	m.eng.mu.RLock()
	names := make([]string, 0, len(m.transports))
	transportByName := make(map[string]core.Transport, len(m.transports))
	for name, t := range m.transports {
		names = append(names, name)
		transportByName[name] = t
	}
	m.eng.mu.RUnlock()

	sort.Strings(names)
	var out []core.DeadLetterEntry
	for _, name := range names {
		src, ok := transportByName[name].(core.IngressDeadLetterSource)
		if !ok || src == nil {
			continue
		}
		for _, entry := range src.CommandDeadLetterEntries() {
			entry.Transport = name
			out = append(out, entry)
		}
	}
	return out
}

// --- CoreCEngine forwarders (core.TransportManager) ---

func (e *CoreCEngine) AddTransport(config core.TransportConfig) error {
	return e.transportMgr.AddTransport(config)
}

func (e *CoreCEngine) RemoveTransport(name string) error {
	return e.transportMgr.RemoveTransport(name)
}

func (e *CoreCEngine) GetTransport(name string) (core.Transport, bool) {
	return e.transportMgr.GetTransport(name)
}

func (e *CoreCEngine) ListTransports() []core.TransportStatus {
	return e.transportMgr.ListTransports()
}
