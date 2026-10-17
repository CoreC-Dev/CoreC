package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func (e *CoreCEngine) Start(ctx context.Context, config *core.Config) error {
	e.mu.Lock()
	e.parentCtx = ctx
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.stats.startTime = time.Now()
	e.status = core.EngineStatusRunning
	// Clear stale state from any previous run (e.g. after Reload→Stop→Start).
	e.driverReg.drivers = make(map[string]core.Driver)
	e.transportMgr.transports = make(map[string]core.Transport)
	e.transportMgr.batchers = make(map[string]*transportBatcher)
	e.transportMgr.transportCancels = make(map[string]context.CancelFunc)
	e.driverReg.tagGroups = make(map[string]map[string]string)
	e.mu.Unlock()

	// Apply engine tuning from global config (overrides defaults).
	if err := e.applyEngineConfig(&config.Global.Engine); err != nil {
		return err
	}

	// Create the command-execution semaphore. This bounds the number of
	// write commands processed in parallel (IMPROVEMENTS #2). Must be
	// created before AddTransport (which starts command listeners).
	if e.writes.commandConcurrency <= 0 {
		e.writes.commandConcurrency = core.DefaultCommandConcurrency
	}
	e.writes.commandSem = make(chan struct{}, e.writes.commandConcurrency)

	// Initialize offline buffer if configured and enabled.
	if config.Global.Buffer.Enabled && config.Global.Buffer.Path != "" {
		ob, err := NewOfflineBuffer(config.Global.Buffer.Path, config.Global.Buffer.MaxSize)
		if err != nil {
			slog.Error("failed to initialize offline buffer, continuing without it", "error", err)
		} else {
			e.transportMgr.offlineBuffer = ob
			slog.Info("offline buffer enabled",
				"path", config.Global.Buffer.Path,
				"max-size", config.Global.Buffer.MaxSize,
				"pending", ob.Len(),
			)
		}
	}

	slog.Info("CoreC engine starting",
		"drivers", len(config.Drivers),
		"transports", len(config.Transports),
		"rules", len(config.Rules),
	)

	// rollback cleans up any partially-started components if Start fails.
	var started bool
	defer func() {
		if started {
			return
		}
		e.rollbackStart()
	}()

	if err := e.lifecycle.startScheduler(); err != nil {
		return err
	}

	// Auto-fill node config: if node.id is set, auto-generate topic-template,
	// command-topic, parser, and forward rules where not explicitly configured.
	e.autoFillNodeConfig(config)

	if err := e.transportMgr.startTransports(config); err != nil {
		return err
	}

	if err := e.ruleSet.initRules(config); err != nil {
		return err
	}

	if err := e.driverReg.startDrivers(config); err != nil {
		return err
	}

	e.processing.startProcessingWorkers()

	// Start derived tag evaluator if configured.
	if len(config.DerivedTags) > 0 {
		if err := e.lifecycle.startDerivedTags(config); err != nil {
			return err
		}
	}

	// Start topology auto-discovery if node config is present.
	e.lifecycle.startDiscovery(config)

	started = true
	slog.Info("CoreC engine started successfully")
	return nil
}

func (e *CoreCEngine) Stop() error {
	slog.Info("CoreC engine stopping")

	if e.cancel != nil {
		e.cancel()
	}

	// Stop discovery module first so it doesn't try to add transports
	// while we're tearing everything else down.
	if e.lifecycle.discovery != nil {
		e.lifecycle.discovery.Stop()
		e.lifecycle.discovery = nil
	}

	// Stop scheduler
	if e.lifecycle.scheduler != nil {
		if err := e.lifecycle.scheduler.Stop(); err != nil {
			slog.Error("failed to stop scheduler", "error", err)
		}
	}

	// Stop all remaining components (drivers, batchers, transports,
	// data bus, rule providers, tag-file watchers).
	e.stopComponents()

	e.wg.Wait()
	e.mu.Lock()
	e.status = core.EngineStatusStopped
	e.lifecycle.derivedEvaluator = nil
	e.mu.Unlock()
	slog.Info("CoreC engine stopped")
	return nil
}

func (e *CoreCEngine) Suspend() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = core.EngineStatusSuspended
	if e.lifecycle.scheduler != nil {
		if err := e.lifecycle.scheduler.Pause(); err != nil {
			return err
		}
	}
	slog.Info("CoreC engine suspended")
	return nil
}

func (e *CoreCEngine) Resume() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = core.EngineStatusRunning
	if e.lifecycle.scheduler != nil {
		if err := e.lifecycle.scheduler.Resume(); err != nil {
			return err
		}
	}
	slog.Info("CoreC engine resumed")
	return nil
}

// rollbackStart cleans up any partially-started components when Start
// fails mid-way. This prevents leaking goroutines and leaving the engine
// in a half-started state (M14). Extracted from Start's defer to keep
// Start focused on orchestration.
//
// Mirrors stopComponents' approach: snapshot under RLock, release before
// calling Stop() (avoids holding the lock during blocking I/O — BE-2),
// and close rule providers + tag-file watchers (BE-1: without these, a
// partial start leaks reload-loop and watcher goroutines).
func (e *CoreCEngine) rollbackStart() {
	slog.Error("engine start failed, rolling back partially started components")
	if e.cancel != nil {
		e.cancel()
	}
	if e.lifecycle.scheduler != nil {
		if err := e.lifecycle.scheduler.Stop(); err != nil {
			slog.Error("rollback: failed to stop scheduler", "error", err)
		}
	}

	// Snapshot under RLock, then release before blocking Stop() calls.
	e.mu.RLock()
	drivers := make([]core.Driver, 0, len(e.driverReg.drivers))
	for _, d := range e.driverReg.drivers {
		drivers = append(drivers, d)
	}
	batchers := make([]*transportBatcher, 0, len(e.transportMgr.batchers))
	for _, b := range e.transportMgr.batchers {
		batchers = append(batchers, b)
	}
	transports := make([]core.Transport, 0, len(e.transportMgr.transports))
	for _, t := range e.transportMgr.transports {
		transports = append(transports, t)
	}
	e.mu.RUnlock()

	stopTimeout := e.shutdownTimeout
	for _, d := range drivers {
		stopWithErrorTimeout("rollback", "driver", stopTimeout, d.Stop)
	}
	for _, b := range batchers {
		e.transportMgr.stopBatcherWithTimeout(b, "rollback", "engine start rollback")
	}
	for _, t := range transports {
		stopWithErrorTimeout("rollback", "transport", stopTimeout, t.Stop)
	}

	// Close rule providers and tag-file watchers — without these, a partial
	// start leaks reload-loop and watcher goroutines (BE-1).
	if e.ruleSet.ruleEngine != nil {
		e.ruleSet.ruleEngine.CloseProviders()
	}
	e.driverReg.stopAllTagFileWatchers()

	if e.processing.dataBus != nil {
		e.processing.dataBus.Close()
	}

	e.mu.Lock()
	e.driverReg.drivers = make(map[string]core.Driver)
	e.transportMgr.transports = make(map[string]core.Transport)
	e.transportMgr.batchers = make(map[string]*transportBatcher)
	e.status = core.EngineStatusStopped
	e.mu.Unlock()

	// Wait for processing/listener goroutines to exit so the engine is
	// fully quiesced before a potential retry (BE-3).
	e.wg.Wait()
}

// stopComponents stops all runtime components (drivers, batchers,
// transports, data bus, rule providers, tag-file watchers) with
// per-component timeouts. Extracted from Stop to keep Stop focused
// on orchestration order. The caller is responsible for cancelling
// the engine context and stopping the scheduler/discovery before
// calling this, and for waiting on e.wg after.
func (e *CoreCEngine) stopComponents() {
	// Snapshot drivers, batchers, and transports under RLock, then release
	// the lock before calling Stop() on each. This prevents blocking I/O
	// from holding the lock and stalling all management API calls (M13).
	e.mu.RLock()
	drivers := make([]core.Driver, 0, len(e.driverReg.drivers))
	driverNames := make([]string, 0, len(e.driverReg.drivers))
	for name, d := range e.driverReg.drivers {
		drivers = append(drivers, d)
		driverNames = append(driverNames, name)
	}
	batchers := make([]*transportBatcher, 0, len(e.transportMgr.batchers))
	batcherNames := make([]string, 0, len(e.transportMgr.batchers))
	for name, b := range e.transportMgr.batchers {
		batchers = append(batchers, b)
		batcherNames = append(batcherNames, name)
	}
	transports := make([]core.Transport, 0, len(e.transportMgr.transports))
	transportNames := make([]string, 0, len(e.transportMgr.transports))
	for name, t := range e.transportMgr.transports {
		transports = append(transports, t)
		transportNames = append(transportNames, name)
	}
	e.mu.RUnlock()

	stopTimeout := e.shutdownTimeout

	// Stop all drivers with timeout
	for i, d := range drivers {
		stopWithErrorTimeout(driverNames[i], "driver", stopTimeout, d.Stop)
	}

	// Empty the data bus before stopping the batchers (stage-B audit
	// findings B1.6/B3.4): once the bus is drained the processing
	// workers have nothing left to consume and exit promptly on the
	// cancelled context, so no worker can still be appending to a
	// batcher's buffer (or enqueueing into its flush queue) while that
	// batcher's final flush runs. Points still queued at this moment are
	// dropped and counted — shutdown abandons not-yet-processed queued
	// data by design (documented in docs/architecture/dataflow.md);
	// until now this drop was completely silent: no log, no counter.
	if dropped := e.drainDataBusOnShutdown(); dropped > 0 {
		e.stats.totalErrors.Add(dropped)
		for range dropped {
			e.stats.statManager.PushError()
		}
		slog.Warn("shutdown: dropped data points still queued in data bus",
			"points", dropped,
		)
	}

	// Stop all batchers with timeout (flush remaining buffered data).
	// Without a timeout, a batcher whose transport ignores context
	// cancellation or has an oversized retry config could hang Stop
	// indefinitely (PERF-008). The shared helper also caps the batcher's
	// own flushFinal budget at the same shutdownTimeout (B3.1).
	for i, b := range batchers {
		e.transportMgr.stopBatcherWithTimeout(b, batcherNames[i], "engine shutdown")
	}

	// Stop all transports with timeout
	for i, t := range transports {
		stopWithErrorTimeout(transportNames[i], "transport", stopTimeout, t.Stop)
	}

	// Close data bus
	if e.processing.dataBus != nil {
		e.processing.dataBus.Close()
	}

	// Close rule providers to stop their reload-loop goroutines.
	if e.ruleSet.ruleEngine != nil {
		e.ruleSet.ruleEngine.CloseProviders()
	}

	// Stop all tag-file watchers to prevent goroutine leaks on reload.
	e.driverReg.stopAllTagFileWatchers()
}

// stopWithErrorTimeout runs stopFn in a goroutine and waits up to timeout for
// it to complete, logging an error on failure or timeout.
func stopWithErrorTimeout(name, kind string, timeout time.Duration, stopFn func() error) {
	done := make(chan error, 1)
	go func() { done <- stopFn() }()
	select {
	case err := <-done:
		if err != nil {
			slog.Error("failed to stop "+kind, "name", name, "error", err)
		}
	case <-time.After(timeout):
		slog.Error("timed out stopping "+kind, "name", name, "timeout", timeout)
	}
}

// drainDataBusOnShutdown removes every point still queued in the data
// bus (main and high-priority channels) and returns how many were
// dropped. Called by stopComponents before the batchers are stopped so
// processing workers quiesce and cannot race with the batchers' final
// flush (stage-B audit findings B1.6/B3.4). The receive loop is
// non-blocking and exits once both channels are momentarily empty;
// workers may still be consuming concurrently, in which case they
// process those points normally and the drain only observes what is
// left. A closed channel (double Stop) is always ready to receive, so
// ok=false returns immediately instead of spinning forever.
func (e *CoreCEngine) drainDataBusOnShutdown() uint64 {
	if e.processing.dataBus == nil {
		return 0
	}
	var dropped uint64
	for {
		select {
		case _, ok := <-e.processing.dataBus.Channel():
			if !ok {
				return dropped
			}
			dropped++
		case _, ok := <-e.processing.dataBus.HighPriorityChannel():
			if !ok {
				return dropped
			}
			dropped++
		default:
			return dropped
		}
	}
}

func (e *CoreCEngine) Reload(config *core.Config) error {
	slog.Info("reloading configuration")
	if err := e.Stop(); err != nil {
		return err
	}
	// Use the saved parent context so SIGTERM can still cancel the reloaded engine.
	e.mu.RLock()
	ctx := e.parentCtx
	e.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return e.Start(ctx, config)
}
