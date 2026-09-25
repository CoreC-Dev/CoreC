package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/core"
)

// --- Transport Management ---

func (e *CoreCEngine) AddTransport(config core.TransportConfig) error {
	transport, err := core.CreateTransport(config)
	if err != nil {
		return fmt.Errorf("failed to create transport %s: %w", config.Name, err)
	}

	if err := transport.Init(e.ctx, config); err != nil {
		return fmt.Errorf("failed to init transport %s: %w", config.Name, err)
	}

	if err := transport.Start(e.ctx); err != nil {
		return fmt.Errorf("failed to start transport %s: %w", config.Name, err)
	}

	e.mu.Lock()
	// If a transport with the same name already exists, remove it and its
	// batcher from the map now so concurrent callers don't see a stopping
	// transport. The actual Stop() happens after Unlock to avoid blocking.
	var oldTransport core.Transport
	var oldBatcher *transportBatcher
	var oldCancel context.CancelFunc
	if t, exists := e.transports[config.Name]; exists {
		oldTransport = t
		delete(e.transports, config.Name)
		if b, hasBatcher := e.batchers[config.Name]; hasBatcher {
			oldBatcher = b
			delete(e.batchers, config.Name)
		}
		if c, hasCancel := e.transportCancels[config.Name]; hasCancel {
			oldCancel = c
			delete(e.transportCancels, config.Name)
		}
	}
	e.mu.Unlock()

	// Cancel the old transport's listener goroutines before stopping it so
	// they don't block forever on the (unclosed) MQTT channels.
	if oldCancel != nil {
		oldCancel()
	}
	// Stop the old transport and batcher outside the lock.
	if oldBatcher != nil {
		oldBatcher.stop()
	}
	if oldTransport != nil {
		if err := oldTransport.Stop(); err != nil {
			slog.Error("failed to stop old transport on replacement", "name", config.Name, "error", err)
		}
	}

	e.mu.Lock()
	e.transports[config.Name] = transport
	e.transportCfg[config.Name] = config

	// Create batcher if batch/flush/retry config is set, or if offline
	// buffering is enabled (so failed publishes are persisted).
	// The engine's publishLatency and dataAge histograms are injected so
	// the batcher's async flush path can observe real transport publish
	// latency and data age (IMPROVEMENTS #1 regression fix). The
	// onBatchPublish callback is injected so the batcher counts real
	// publishes (not just enqueues) on the async flush path (Finding 4
	// fix: previously publish.go counted prematurely).
	if b := newTransportBatcher(transport, config, e.offlineBuffer, e.publishLatency, e.dataAge, e.onBatchPublish); b != nil {
		b.start(e.ctx)
		e.batchers[config.Name] = b
		slog.Info("transport batching enabled",
			"name", config.Name,
			"batch-size", config.BatchSize,
			"flush-interval", config.FlushInterval,
			"retry-count", config.RetryCount,
		)
	}

	e.mu.Unlock()

	// Start command and data listeners for this transport if the engine is
	// already running.  This fixes M29: transports added after Start() now
	// receive write commands and ingest data.
	//
	// Each transport gets its own child context derived from e.ctx so that
	// RemoveTransport can cancel just this transport's listeners without
	// tearing down the whole engine. This fixes the goroutine leak where
	// MQTT listener goroutines blocked forever after RemoveTransport.
	if e.ctx != nil && e.ctx.Err() == nil {
		tCtx, tCancel := context.WithCancel(e.ctx)
		e.mu.Lock()
		e.transportCancels[config.Name] = tCancel
		e.mu.Unlock()
		e.startCommandListener(transport, tCtx)
		e.startDataListener(transport, tCtx)
	}

	slog.Info("transport added", "name", config.Name, "type", config.Type)
	return nil
}

func (e *CoreCEngine) RemoveTransport(name string) error {
	e.mu.Lock()
	transport, ok := e.transports[name]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("transport not found: %s", name)
	}
	delete(e.transports, name)
	delete(e.transportCfg, name)

	// Also remove and stop the associated batcher so its flushLoop
	// goroutine doesn't keep running and try to publish to the
	// stopped transport. Without this, RemoveTransport leaks the
	// batcher goroutine and publishToTargets may still route data
	// through the stale batcher (problem 1+2).
	var batcher *transportBatcher
	if b, hasBatcher := e.batchers[name]; hasBatcher {
		batcher = b
		delete(e.batchers, name)
	}

	// Cancel this transport's listener goroutines so they exit instead of
	// blocking forever on the (unclosed) MQTT command/data channels. This
	// fixes the goroutine leak: the engine ctx stays alive (only full Stop
	// cancels it) and MQTTTransport.Stop deliberately does not close its
	// channels, so without per-transport cancellation the
	// startCommandListener/startDataListener goroutines would block forever.
	var tCancel context.CancelFunc
	if c, hasCancel := e.transportCancels[name]; hasCancel {
		tCancel = c
		delete(e.transportCancels, name)
	}
	e.mu.Unlock()

	if tCancel != nil {
		tCancel()
	}

	if batcher != nil {
		batcher.stop()
	}
	return transport.Stop()
}

func (e *CoreCEngine) GetTransport(name string) (core.Transport, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	t, ok := e.transports[name]
	return t, ok
}

func (e *CoreCEngine) ListTransports() []core.TransportStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]core.TransportStatus, 0, len(e.transports))
	for _, t := range e.transports {
		result = append(result, t.Status())
	}
	return result
}

// startDataListener launches a goroutine that listens for data points from
// a transport's OnData channel and feeds them into the DataBus — the
// chained-core inbound path.  DataPoints are piped back to the
// processing loop via a channel so chained transports feed the same
// pipeline as driver-sourced data.
//
// It is called from AddTransport for every transport (initial and runtime).
func (e *CoreCEngine) startDataListener(t core.Transport, tCtx context.Context) {
	dataCh := t.OnData()
	if dataCh == nil {
		return
	}
	e.wg.Add(1)
	go func(ch <-chan core.DataPoint, transportName string) {
		defer e.wg.Done()
		slog.Info("data listener started", "transport", transportName)
		for {
			select {
			case <-tCtx.Done():
				return
			case point, ok := <-ch:
				if !ok {
					return
				}
				e.totalRead.Add(1)
				e.dataBus.Push(point)
			}
		}
	}(dataCh, t.Name())
}
