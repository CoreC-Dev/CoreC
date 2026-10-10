package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/rule"
)

func (e *CoreCEngine) Start(ctx context.Context, config *core.Config) error {
	e.mu.Lock()
	e.parentCtx = ctx
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.startTime = time.Now()
	e.status = core.EngineStatusRunning
	// Clear stale state from any previous run (e.g. after Reload→Stop→Start).
	e.drivers = make(map[string]core.Driver)
	e.transports = make(map[string]core.Transport)
	e.batchers = make(map[string]*transportBatcher)
	e.transportCancels = make(map[string]context.CancelFunc)
	e.tagGroups = make(map[string]map[string]string)
	e.mu.Unlock()

	// Apply engine tuning from global config (overrides defaults).
	e.applyEngineConfig(&config.Global.Engine)

	// Create the command-execution semaphore. This bounds the number of
	// write commands processed in parallel (IMPROVEMENTS #2). Must be
	// created before AddTransport (which starts command listeners).
	if e.commandConcurrency <= 0 {
		e.commandConcurrency = core.DefaultCommandConcurrency
	}
	e.commandSem = make(chan struct{}, e.commandConcurrency)

	// Initialize offline buffer if configured and enabled.
	if config.Global.Buffer.Enabled && config.Global.Buffer.Path != "" {
		ob, err := NewOfflineBuffer(config.Global.Buffer.Path, config.Global.Buffer.MaxSize)
		if err != nil {
			slog.Error("failed to initialize offline buffer, continuing without it", "error", err)
		} else {
			e.offlineBuffer = ob
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

	if err := e.startScheduler(); err != nil {
		return err
	}

	// Auto-fill node config: if node.id is set, auto-generate topic-template,
	// command-topic, parser, and forward rules where not explicitly configured.
	e.autoFillNodeConfig(config)

	if err := e.startTransports(config); err != nil {
		return err
	}

	if err := e.initRules(config); err != nil {
		return err
	}

	if err := e.startDrivers(config); err != nil {
		return err
	}

	e.startProcessingWorkers()

	// Start topology auto-discovery if node config is present.
	e.startDiscovery(config)

	started = true
	slog.Info("CoreC engine started successfully")
	return nil
}

// startScheduler creates the data bus and scheduler, then starts the
// scheduler goroutine.
func (e *CoreCEngine) startScheduler() error {
	e.mu.Lock()
	e.dataBus = NewDataBus(e.dataBusSize)
	e.scheduler = NewScheduler(e.readFromDriver, e.onDriverData, e.errorThrottleWin, e.statManager)
	e.mu.Unlock()

	if err := e.scheduler.Start(e.ctx); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}
	return nil
}

// startTransports initializes and adds all configured transports.
func (e *CoreCEngine) startTransports(config *core.Config) error {
	for _, tc := range config.Transports {
		if err := e.AddTransport(tc); err != nil {
			slog.Error("failed to add transport", "name", tc.Name, "error", err)
			return fmt.Errorf("failed to add transport %s: %w", tc.Name, err)
		}
	}
	return nil
}

// initRules sets up rule providers, sub-rule groups, and top-level rules.
func (e *CoreCEngine) initRules(config *core.Config) error {
	for _, pc := range config.RuleProviders {
		p, err := rule.NewFileProvider(pc.Name, pc.Path, pc.Interval)
		if err != nil {
			slog.Error("failed to create rule provider", "name", pc.Name, "error", err)
			return fmt.Errorf("failed to create rule provider %s: %w", pc.Name, err)
		}
		e.ruleEngine.AddProvider(p)
	}

	if len(config.RuleGroups) > 0 {
		if err := e.ruleEngine.SetSubRules(config.RuleGroups); err != nil {
			return fmt.Errorf("failed to set sub-rules: %w", err)
		}
	}

	if err := e.SetRules(config.Rules); err != nil {
		return fmt.Errorf("failed to set rules: %w", err)
	}
	return nil
}

// startDrivers initializes and starts all configured drivers.
func (e *CoreCEngine) startDrivers(config *core.Config) error {
	for _, dc := range config.Drivers {
		if err := e.AddDriver(dc); err != nil {
			slog.Error("failed to add driver", "name", dc.Name, "error", err)
			return fmt.Errorf("failed to add driver %s: %w", dc.Name, err)
		}
	}
	return nil
}

// startProcessingWorkers launches the N worker goroutines that consume
// from the DataBus channel, plus optional high-priority workers.
func (e *CoreCEngine) startProcessingWorkers() {
	numWorkers := e.numWorkers
	if numWorkers < 1 {
		numWorkers = 1
	}
	e.wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go e.processingLoop()
	}

	numHP := e.numHighPriorityWorkers
	if numHP < 0 {
		numHP = 0
	}
	if numHP > 0 {
		e.wg.Add(numHP)
		for i := 0; i < numHP; i++ {
			go e.processingLoopHighPriority()
		}
		slog.Info("high-priority processing workers started", "count", numHP)
	}
}

func (e *CoreCEngine) Stop() error {
	slog.Info("CoreC engine stopping")

	if e.cancel != nil {
		e.cancel()
	}

	// Stop discovery module first so it doesn't try to add transports
	// while we're tearing everything else down.
	if e.discovery != nil {
		e.discovery.Stop()
		e.discovery = nil
	}

	// Stop scheduler
	if e.scheduler != nil {
		if err := e.scheduler.Stop(); err != nil {
			slog.Error("failed to stop scheduler", "error", err)
		}
	}

	// Stop all remaining components (drivers, batchers, transports,
	// data bus, rule providers, tag-file watchers).
	e.stopComponents()

	e.wg.Wait()
	e.mu.Lock()
	e.status = core.EngineStatusStopped
	e.mu.Unlock()
	slog.Info("CoreC engine stopped")
	return nil
}

func (e *CoreCEngine) Suspend() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = core.EngineStatusSuspended
	if e.scheduler != nil {
		if err := e.scheduler.Pause(); err != nil {
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
	if e.scheduler != nil {
		if err := e.scheduler.Resume(); err != nil {
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
	if e.scheduler != nil {
		if err := e.scheduler.Stop(); err != nil {
			slog.Error("rollback: failed to stop scheduler", "error", err)
		}
	}

	// Snapshot under RLock, then release before blocking Stop() calls.
	e.mu.RLock()
	drivers := make([]core.Driver, 0, len(e.drivers))
	for _, d := range e.drivers {
		drivers = append(drivers, d)
	}
	batchers := make([]*transportBatcher, 0, len(e.batchers))
	for _, b := range e.batchers {
		batchers = append(batchers, b)
	}
	transports := make([]core.Transport, 0, len(e.transports))
	for _, t := range e.transports {
		transports = append(transports, t)
	}
	e.mu.RUnlock()

	stopTimeout := e.shutdownTimeout
	for _, d := range drivers {
		stopWithErrorTimeout("rollback", "driver", stopTimeout, d.Stop)
	}
	for _, b := range batchers {
		done := make(chan struct{}, 1)
		go func() { b.stop(); done <- struct{}{} }()
		select {
		case <-done:
		case <-time.After(stopTimeout):
			slog.Error("rollback: timed out stopping batcher", "timeout", stopTimeout)
		}
	}
	for _, t := range transports {
		stopWithErrorTimeout("rollback", "transport", stopTimeout, t.Stop)
	}

	// Close rule providers and tag-file watchers — without these, a partial
	// start leaks reload-loop and watcher goroutines (BE-1).
	if e.ruleEngine != nil {
		e.ruleEngine.CloseProviders()
	}
	e.stopAllTagFileWatchers()

	if e.dataBus != nil {
		e.dataBus.Close()
	}

	e.mu.Lock()
	e.drivers = make(map[string]core.Driver)
	e.transports = make(map[string]core.Transport)
	e.batchers = make(map[string]*transportBatcher)
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
	drivers := make([]core.Driver, 0, len(e.drivers))
	driverNames := make([]string, 0, len(e.drivers))
	for name, d := range e.drivers {
		drivers = append(drivers, d)
		driverNames = append(driverNames, name)
	}
	batchers := make([]*transportBatcher, 0, len(e.batchers))
	batcherNames := make([]string, 0, len(e.batchers))
	for name, b := range e.batchers {
		batchers = append(batchers, b)
		batcherNames = append(batcherNames, name)
	}
	transports := make([]core.Transport, 0, len(e.transports))
	transportNames := make([]string, 0, len(e.transports))
	for name, t := range e.transports {
		transports = append(transports, t)
		transportNames = append(transportNames, name)
	}
	e.mu.RUnlock()

	stopTimeout := e.shutdownTimeout

	// Stop all drivers with timeout
	for i, d := range drivers {
		stopWithErrorTimeout(driverNames[i], "driver", stopTimeout, d.Stop)
	}

	// Stop all batchers with timeout (flush remaining buffered data).
	// Without a timeout, a batcher whose transport ignores context
	// cancellation or has an oversized retry config could hang Stop
	// indefinitely (PERF-008).
	for i, b := range batchers {
		done := make(chan struct{}, 1)
		go func() { b.stop(); done <- struct{}{} }()
		select {
		case <-done:
			slog.Info("batcher stopped", "name", batcherNames[i])
		case <-time.After(stopTimeout):
			slog.Error("timed out stopping batcher", "name", batcherNames[i], "timeout", stopTimeout)
		}
	}

	// Stop all transports with timeout
	for i, t := range transports {
		stopWithErrorTimeout(transportNames[i], "transport", stopTimeout, t.Stop)
	}

	// Close data bus
	if e.dataBus != nil {
		e.dataBus.Close()
	}

	// Close rule providers to stop their reload-loop goroutines.
	if e.ruleEngine != nil {
		e.ruleEngine.CloseProviders()
	}

	// Stop all tag-file watchers to prevent goroutine leaks on reload.
	e.stopAllTagFileWatchers()
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
