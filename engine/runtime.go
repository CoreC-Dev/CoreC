// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// engineRuntime groups the engine's lifecycle runtime state and the
// Start/Stop/Reload orchestration: the status, the root context and
// cancellation, the goroutine wait group, the shutdown timeout, and the
// config application. Extracted from CoreCEngine in the CPLX-020 God
// Object split: CoreCEngine is now a composition shell that holds the
// shared lock plus the nine role structs and forwards the core.Engine
// method set to them.
type engineRuntime struct {
	status core.EngineStatus

	// Lifecycle
	ctx       context.Context
	parentCtx context.Context // saved from Start() for Reload()
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// shutdownTimeout is the maximum time to wait for drivers and
	// transports to stop during graceful shutdown.
	shutdownTimeout time.Duration

	// eng is the owning engine: the orchestrators reach every other
	// role (driver registry, transport manager, rules, stats, writes,
	// processing, lifecycle support) and the shared lock through it.
	eng *CoreCEngine
}

func (r *engineRuntime) Start(ctx context.Context, config *core.Config) error {
	// The parent/ctx/cancel/status writes are inside the same critical
	// section as the stale-state clears: concurrent Stats()/management
	// readers must never observe a half-transitioned runtime (this lock
	// scope is inherited verbatim from the pre-split CoreCEngine.Start).
	r.eng.mu.Lock()
	r.parentCtx = ctx
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.eng.stats.startTime = time.Now()
	r.status = core.EngineStatusRunning
	// Clear stale state from any previous run (e.g. after Reload→Stop→Start).
	r.eng.driverReg.drivers = make(map[string]core.Driver)
	r.eng.transportMgr.transports = make(map[string]core.Transport)
	r.eng.transportMgr.batchers = make(map[string]*transportBatcher)
	r.eng.transportMgr.transportCancels = make(map[string]context.CancelFunc)
	r.eng.driverReg.tagGroups = make(map[string]map[string]string)
	r.eng.mu.Unlock()

	// Apply engine tuning from global config (overrides defaults).
	if err := r.applyEngineConfig(&config.Global.Engine); err != nil {
		return err
	}

	// Create the command-execution semaphore. This bounds the number of
	// write commands processed in parallel (IMPROVEMENTS #2). Must be
	// created before AddTransport (which starts command listeners).
	if r.eng.writes.commandConcurrency <= 0 {
		r.eng.writes.commandConcurrency = core.DefaultCommandConcurrency
	}
	r.eng.writes.commandSem = make(chan struct{}, r.eng.writes.commandConcurrency)

	// Initialize offline buffer if configured and enabled.
	if config.Global.Buffer.Enabled && config.Global.Buffer.Path != "" {
		ob, err := NewOfflineBuffer(config.Global.Buffer.Path, config.Global.Buffer.MaxSize)
		if err != nil {
			slog.Error("failed to initialize offline buffer, continuing without it", "error", err)
		} else {
			r.eng.transportMgr.offlineBuffer = ob
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
		r.rollbackStart()
	}()

	if err := r.eng.lifecycle.startScheduler(); err != nil {
		return err
	}

	// Auto-fill node config: if node.id is set, auto-generate topic-template,
	// command-topic, parser, and forward rules where not explicitly configured.
	r.autoFillNodeConfig(config)

	if err := r.eng.transportMgr.startTransports(config); err != nil {
		return err
	}

	if err := r.eng.ruleSet.initRules(config); err != nil {
		return err
	}

	if err := r.eng.driverReg.startDrivers(config); err != nil {
		return err
	}

	r.eng.processing.startProcessingWorkers()

	// Build this run's alert registry (audit OBS-4/HUB-6) with the
	// configured active window and start its sweep loop. It is rebuilt
	// on every Start alongside the rest of the pipeline state.
	r.eng.processing.alerts = newAlertManager(r.eng.processing.alertActiveWindow)
	r.eng.processing.alerts.start(r.ctx)

	// Start derived tag evaluator if configured.
	if len(config.DerivedTags) > 0 {
		if err := r.eng.lifecycle.startDerivedTags(config); err != nil {
			return err
		}
	}

	// Start topology auto-discovery if node config is present.
	r.eng.lifecycle.startDiscovery(config)

	started = true
	slog.Info("CoreC engine started successfully")
	return nil
}

func (r *engineRuntime) Stop() error {
	slog.Info("CoreC engine stopping")

	if r.cancel != nil {
		r.cancel()
	}

	// Stop discovery module first so it doesn't try to add transports
	// while we're tearing everything else down.
	if r.eng.lifecycle.discovery != nil {
		r.eng.lifecycle.discovery.Stop()
		r.eng.lifecycle.discovery = nil
	}

	// Stop the derived tag evaluator: its goroutines run on the
	// evaluator's own wait group (hot-reload support), so they are
	// joined here explicitly rather than by the engine-wide wg.Wait.
	// It must stop before stopComponents closes the data bus it
	// pushes into.
	if ev := r.eng.lifecycle.swapDerivedEvaluator(nil); ev != nil {
		ev.stop()
	}

	// Stop scheduler
	if r.eng.lifecycle.scheduler != nil {
		if err := r.eng.lifecycle.scheduler.Stop(); err != nil {
			slog.Error("failed to stop scheduler", "error", err)
		}
	}

	// Stop all remaining components (drivers, batchers, transports,
	// data bus, rule providers, tag-file watchers).
	r.stopComponents()

	r.wg.Wait()

	// Stop the alert registry last: the engine-wide wait group has
	// joined every processing worker, so no record() can race the
	// event-channel close (audit OBS-4).
	if r.eng.processing.alerts != nil {
		r.eng.processing.alerts.stop()
	}

	r.eng.mu.Lock()
	r.status = core.EngineStatusStopped
	r.eng.mu.Unlock()
	slog.Info("CoreC engine stopped")
	return nil
}

func (r *engineRuntime) Suspend() error {
	r.eng.mu.Lock()
	defer r.eng.mu.Unlock()
	r.status = core.EngineStatusSuspended
	if r.eng.lifecycle.scheduler != nil {
		if err := r.eng.lifecycle.scheduler.Pause(); err != nil {
			return err
		}
	}
	slog.Info("CoreC engine suspended")
	return nil
}

func (r *engineRuntime) Resume() error {
	r.eng.mu.Lock()
	defer r.eng.mu.Unlock()
	r.status = core.EngineStatusRunning
	if r.eng.lifecycle.scheduler != nil {
		if err := r.eng.lifecycle.scheduler.Resume(); err != nil {
			return err
		}
	}
	slog.Info("CoreC engine resumed")
	return nil
}

func (r *engineRuntime) Status() core.EngineStatus {
	r.eng.mu.RLock()
	defer r.eng.mu.RUnlock()
	return r.status
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
func (r *engineRuntime) rollbackStart() {
	slog.Error("engine start failed, rolling back partially started components")
	if r.cancel != nil {
		r.cancel()
	}
	if r.eng.lifecycle.scheduler != nil {
		if err := r.eng.lifecycle.scheduler.Stop(); err != nil {
			slog.Error("rollback: failed to stop scheduler", "error", err)
		}
	}

	// Snapshot under RLock, then release before blocking Stop() calls.
	r.eng.mu.RLock()
	drivers := make([]core.Driver, 0, len(r.eng.driverReg.drivers))
	for _, d := range r.eng.driverReg.drivers {
		drivers = append(drivers, d)
	}
	batchers := make([]*transportBatcher, 0, len(r.eng.transportMgr.batchers))
	for _, b := range r.eng.transportMgr.batchers {
		batchers = append(batchers, b)
	}
	transports := make([]core.Transport, 0, len(r.eng.transportMgr.transports))
	for _, t := range r.eng.transportMgr.transports {
		transports = append(transports, t)
	}
	r.eng.mu.RUnlock()

	stopTimeout := r.shutdownTimeout
	for _, d := range drivers {
		stopWithErrorTimeout("rollback", "driver", stopTimeout, d.Stop)
	}
	for _, b := range batchers {
		r.eng.transportMgr.stopBatcherWithTimeout(b, "rollback", "engine start rollback")
	}
	for _, t := range transports {
		stopWithErrorTimeout("rollback", "transport", stopTimeout, t.Stop)
	}

	// Close rule providers and tag-file watchers — without these, a partial
	// start leaks reload-loop and watcher goroutines (BE-1).
	if r.eng.ruleSet.ruleEngine != nil {
		r.eng.ruleSet.ruleEngine.CloseProviders()
	}
	r.eng.driverReg.stopAllTagFileWatchers()

	if r.eng.processing.dataBus != nil {
		r.eng.processing.dataBus.Close()
	}

	r.eng.mu.Lock()
	r.eng.driverReg.drivers = make(map[string]core.Driver)
	r.eng.transportMgr.transports = make(map[string]core.Transport)
	r.eng.transportMgr.batchers = make(map[string]*transportBatcher)
	r.status = core.EngineStatusStopped
	r.eng.mu.Unlock()

	// Wait for processing/listener goroutines to exit so the engine is
	// fully quiesced before a potential retry (BE-3).
	r.wg.Wait()
}

// stopComponents stops all runtime components (drivers, batchers,
// transports, data bus, rule providers, tag-file watchers) with
// per-component timeouts. Extracted from Stop to keep Stop focused
// on orchestration order. The caller is responsible for cancelling
// the engine context and stopping the scheduler/discovery before
// calling this, and for waiting on the wait group after.
func (r *engineRuntime) stopComponents() {
	// Snapshot drivers, batchers, and transports under RLock, then release
	// the lock before calling Stop() on each. This prevents blocking I/O
	// from holding the lock and stalling all management API calls (M13).
	r.eng.mu.RLock()
	drivers := make([]core.Driver, 0, len(r.eng.driverReg.drivers))
	driverNames := make([]string, 0, len(r.eng.driverReg.drivers))
	for name, d := range r.eng.driverReg.drivers {
		drivers = append(drivers, d)
		driverNames = append(driverNames, name)
	}
	batchers := make([]*transportBatcher, 0, len(r.eng.transportMgr.batchers))
	batcherNames := make([]string, 0, len(r.eng.transportMgr.batchers))
	for name, b := range r.eng.transportMgr.batchers {
		batchers = append(batchers, b)
		batcherNames = append(batcherNames, name)
	}
	transports := make([]core.Transport, 0, len(r.eng.transportMgr.transports))
	transportNames := make([]string, 0, len(r.eng.transportMgr.transports))
	for name, t := range r.eng.transportMgr.transports {
		transports = append(transports, t)
		transportNames = append(transportNames, name)
	}
	r.eng.mu.RUnlock()

	stopTimeout := r.shutdownTimeout

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
	if dropped := r.drainDataBusOnShutdown(); dropped > 0 {
		r.eng.stats.totalErrors.Add(dropped)
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
		r.eng.transportMgr.stopBatcherWithTimeout(b, batcherNames[i], "engine shutdown")
	}

	// Stop all transports with timeout
	for i, t := range transports {
		stopWithErrorTimeout(transportNames[i], "transport", stopTimeout, t.Stop)
	}

	// Close data bus
	if r.eng.processing.dataBus != nil {
		r.eng.processing.dataBus.Close()
	}

	// Close rule providers to stop their reload-loop goroutines.
	if r.eng.ruleSet.ruleEngine != nil {
		r.eng.ruleSet.ruleEngine.CloseProviders()
	}

	// Stop all tag-file watchers to prevent goroutine leaks on reload.
	r.eng.driverReg.stopAllTagFileWatchers()
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
func (r *engineRuntime) drainDataBusOnShutdown() uint64 {
	if r.eng.processing.dataBus == nil {
		return 0
	}
	var dropped uint64
	for {
		select {
		case _, ok := <-r.eng.processing.dataBus.Channel():
			if !ok {
				return dropped
			}
			dropped++
		case _, ok := <-r.eng.processing.dataBus.HighPriorityChannel():
			if !ok {
				return dropped
			}
			dropped++
		default:
			return dropped
		}
	}
}

func (r *engineRuntime) Reload(config *core.Config) error {
	slog.Info("reloading configuration")
	if err := r.Stop(); err != nil {
		return err
	}
	// Use the saved parent context so SIGTERM can still cancel the reloaded engine.
	r.eng.mu.RLock()
	ctx := r.parentCtx
	r.eng.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return r.Start(ctx, config)
}

// applyEngineConfig overrides engine tuning parameters from the global
// config. Zero/empty values fall back to the defaults already set in New().
func (r *engineRuntime) applyEngineConfig(cfg *core.EngineConfig) error {
	if cfg.DataBusSize > 0 {
		r.eng.processing.dataBusSize = cfg.DataBusSize
	}
	if cfg.Workers > 0 {
		r.eng.processing.numWorkers = cfg.Workers
	}
	if d, err := time.ParseDuration(cfg.ShutdownTimeout); err == nil && d > 0 {
		r.shutdownTimeout = d
	}
	if d, err := time.ParseDuration(cfg.ErrorThrottleWindow); err == nil && d > 0 {
		r.eng.lifecycle.errorThrottleWin = d
	}
	if d, err := time.ParseDuration(cfg.DefaultTagInterval); err == nil && d > 0 {
		r.eng.lifecycle.defaultTagInterval = d
	}
	if cfg.OnBadQuality != "" {
		p, err := parseBadQualityPolicy(cfg.OnBadQuality)
		if err != nil {
			return err
		}
		r.eng.processing.badQualityPolicy = p
	}
	if d, err := time.ParseDuration(cfg.StaleThreshold); err == nil && d > 0 {
		r.eng.reads.staleThreshold.Store(int64(d))
	}
	if cfg.WriteRetryCount > 0 {
		r.eng.writes.writeRetryCount = cfg.WriteRetryCount
	}
	if cfg.CommandConcurrency > 0 {
		r.eng.writes.commandConcurrency = cfg.CommandConcurrency
	}
	if cfg.HighPriorityWorkers > 0 {
		r.eng.processing.numHighPriorityWorkers = cfg.HighPriorityWorkers
	}
	if d, err := time.ParseDuration(cfg.AlertActiveWindow); err == nil && d > 0 {
		r.eng.processing.alertActiveWindow = d
	}
	return nil
}

// autoFillNodeConfig auto-generates configuration fields that are omitted
// when node auto-discovery is enabled (node.id is set).
//
// Principle: "explicit overrides auto". If a field is already set in the
// config, it is left untouched. Only omitted fields are auto-generated.
//
// Auto-generated fields:
//   - topic-template: "{prefix}/{node-id}/data/{{.Driver}}/{{.Tag}}"
//   - command-topic:  "{prefix}/{node-id}/cmd/#"
//   - parser:         { type: default } (only when data-topic is set)
//   - forward rule:   match ALL → first transport (when no rules exist)
func (r *engineRuntime) autoFillNodeConfig(config *core.Config) {
	if config.Node.ID == "" {
		return // auto-discovery disabled
	}

	prefix := config.Node.TopicPrefix
	if prefix == "" {
		prefix = "topo"
	}

	for i := range config.Transports {
		tc := &config.Transports[i]
		if tc.Type != "mqtt" {
			continue
		}

		// Auto-fill topic-template (outbound publish topic).
		if _, ok := tc.Settings["topic-template"]; !ok {
			tc.Settings["topic-template"] = fmt.Sprintf("%s/%s/data/{{.Driver}}/{{.Tag}}", prefix, config.Node.ID)
			slog.Debug("auto-filled topic-template", "transport", tc.Name, "node", config.Node.ID)
		}

		// Auto-fill command-topic (for receiving write commands).
		if _, ok := tc.Settings["command-topic"]; !ok {
			tc.Settings["command-topic"] = fmt.Sprintf("%s/%s/cmd/#", prefix, config.Node.ID)
			slog.Debug("auto-filled command-topic", "transport", tc.Name, "node", config.Node.ID)
		}

		// Auto-fill parser for transports with explicit data-topic (third-party inbound).
		if _, hasDataTopic := tc.Settings["data-topic"]; hasDataTopic {
			if _, hasParser := tc.Settings["parser"]; !hasParser {
				tc.Settings["parser"] = map[string]any{"type": "default"}
				slog.Debug("auto-filled parser", "transport", tc.Name, "type", "default")
			}
		}
	}

	// Auto-add a forward-all rule if no rules are configured.
	// This lets collector nodes publish data without manually writing a rule.
	if len(config.Rules) == 0 && len(config.Transports) > 0 {
		config.Rules = []core.RuleConfig{
			{Name: "topo-auto-forward", Match: "ALL", Action: "forward", Target: config.Transports[0].Name},
		}
		slog.Debug("auto-added forward rule", "target", config.Transports[0].Name)
	}
}
