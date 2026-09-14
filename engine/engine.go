package engine

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine/statistic"
	"github.com/CoreC-Dev/CoreC/log"
	"github.com/CoreC-Dev/CoreC/rule"
)

var _ core.Engine = (*CoreCEngine)(nil)

// CoreCEngine implements core.Engine.
// This is the central orchestrator.
type CoreCEngine struct {
	mu sync.RWMutex

	// Operational State
	status core.EngineStatus

	// Components
	drivers      map[string]core.Driver
	transports   map[string]core.Transport
	transportCfg map[string]core.TransportConfig // for fallback lookups
	batchers     map[string]*transportBatcher
	ruleEngine   *rule.Engine
	scheduler    *scheduler
	dataBus      *DataBus
	cache        *LatestCache

	// tagGroups maps driver name -> tag name -> group.
	// Populated from TagConfig.Group in AddDriver and consulted in
	// onDriverData to enrich DataPoint.Group. The scheduler only forwards
	// []TagValue (which carries the tag name but not its group), so without
	// this map the Group would always be empty — breaking rules like
	// "group == 'reactor'" and MQTT topic templates like ".../{{.Group}}/...".
	tagGroups map[string]map[string]string

	// Alert handlers
	alertHandlers []func(point core.DataPoint, rule core.Rule)

	// Stats
	totalRead    atomic.Uint64
	totalPublish atomic.Uint64
	totalErrors  atomic.Uint64
	startTime    time.Time

	// Processing parallelism
	numWorkers int

	// Tunable parameters (set from GlobalConfig.Engine in Start)
	dataBusSize        int
	shutdownTimeout    time.Duration
	errorThrottleWin   time.Duration
	defaultTagInterval time.Duration
	badQualityPolicy   badQualityPolicy
	staleThreshold     time.Duration // 0 = disabled
	writeRetryCount    int

	// offlineBuffer persists failed publish batches to disk for replay
	// after transport recovery. nil = disabled (no buffer config).
	offlineBuffer *OfflineBuffer

	// discovery handles topology auto-discovery via MQTT heartbeats.
	// nil when node config is not set (auto-discovery disabled).
	discovery *Discovery

	// deadLetterQueue holds write commands that failed after all retries.
	// Bounded by deadLetterMaxLen; oldest entries are evicted when full.
	deadLetterMu     sync.Mutex
	deadLetterQueue  []core.DeadLetterEntry
	deadLetterMaxLen int

	// tagFileWatchers tracks per-driver hot-reload watchers for tags-file.
	// Keyed by driver name; nil entry means no watcher for that driver.
	tagFileWatchers map[string]*tagFileWatcher

	// driverConfigs stores the last-applied DriverConfig per driver name,
	// so tag-file watchers can rebuild the config with updated tags.
	driverConfigs map[string]core.DriverConfig

	// Lifecycle
	ctx       context.Context
	parentCtx context.Context // saved from Start() for Reload()
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// badQualityPolicy controls how DataPoints with QualityBad are handled.
type badQualityPolicy int

const (
	badQualityPublish       badQualityPolicy = iota // forward normally (default)
	badQualityDrop                                  // discard before rule matching
	badQualityMarkAndPublish                        // set Value=nil, keep quality=bad, forward
	badQualityAlert                                // forward + trigger alert callback
)

func parseBadQualityPolicy(s string) badQualityPolicy {
	switch strings.ToLower(s) {
	case "drop":
		return badQualityDrop
	case "mark-and-publish":
		return badQualityMarkAndPublish
	case "alert":
		return badQualityAlert
	default:
		return badQualityPublish
	}
}

// New creates a new CoreC Engine instance.
func New() core.Engine {
	return &CoreCEngine{
		status:             core.EngineStatusStopped,
		drivers:            make(map[string]core.Driver),
		transports:         make(map[string]core.Transport),
		transportCfg:       make(map[string]core.TransportConfig),
		batchers:           make(map[string]*transportBatcher),
		tagGroups:          make(map[string]map[string]string),
		ruleEngine:         rule.NewEngine(),
		cache:              NewLatestCache(),
		numWorkers:         runtime.NumCPU(),
		dataBusSize:        core.DefaultDataBusSize,
		shutdownTimeout:    core.DefaultShutdownTimeout,
		errorThrottleWin:   core.DefaultErrorThrottleWindow,
		defaultTagInterval: core.DefaultTagInterval,
		badQualityPolicy:   badQualityPublish,
		writeRetryCount:    3,
		deadLetterMaxLen:   1000,
		tagFileWatchers:    make(map[string]*tagFileWatcher),
		driverConfigs:      make(map[string]core.DriverConfig),
	}
}

// applyEngineConfig overrides engine tuning parameters from the global
// config. Zero/empty values fall back to the defaults already set in New().
func (e *CoreCEngine) applyEngineConfig(cfg *core.EngineConfig) {
	if cfg.DataBusSize > 0 {
		e.dataBusSize = cfg.DataBusSize
	}
	if cfg.Workers > 0 {
		e.numWorkers = cfg.Workers
	}
	if d, err := time.ParseDuration(cfg.ShutdownTimeout); err == nil && d > 0 {
		e.shutdownTimeout = d
	}
	if d, err := time.ParseDuration(cfg.ErrorThrottleWindow); err == nil && d > 0 {
		e.errorThrottleWin = d
	}
	if d, err := time.ParseDuration(cfg.DefaultTagInterval); err == nil && d > 0 {
		e.defaultTagInterval = d
	}
	if cfg.OnBadQuality != "" {
		e.badQualityPolicy = parseBadQualityPolicy(cfg.OnBadQuality)
	}
	if d, err := time.ParseDuration(cfg.StaleThreshold); err == nil && d > 0 {
		e.staleThreshold = d
	}
	if cfg.WriteRetryCount > 0 {
		e.writeRetryCount = cfg.WriteRetryCount
	}
}

func (e *CoreCEngine) Start(ctx context.Context, config *core.Config) error {
	e.mu.Lock()
	e.parentCtx = ctx
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.startTime = time.Now()
	e.status = core.EngineStatusRunning
	// Clear stale state from any previous run (e.g. after Reload→Stop→Start).
	// Stop() stops all drivers/transports but does not clear the maps, so
	// without this reset, Reload would accumulate old entries alongside new ones.
	e.drivers = make(map[string]core.Driver)
	e.transports = make(map[string]core.Transport)
	e.batchers = make(map[string]*transportBatcher)
	e.tagGroups = make(map[string]map[string]string)
	e.mu.Unlock()

	// Apply engine tuning from global config (overrides defaults).
	e.applyEngineConfig(&config.Global.Engine)

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
	// This prevents leaking goroutines and leaving the engine in a half-started state (M14).
	var started bool
	defer func() {
		if started {
			return
		}
		slog.Error("engine start failed, rolling back partially started components")
		if e.cancel != nil {
			e.cancel()
		}
		if e.scheduler != nil {
			if err := e.scheduler.Stop(); err != nil {
				slog.Error("rollback: failed to stop scheduler", "error", err)
			}
		}
		e.mu.Lock()
		for name, d := range e.drivers {
			if err := d.Stop(); err != nil {
				slog.Error("rollback: failed to stop driver", "name", name, "error", err)
			}
		}
		for _, b := range e.batchers {
			b.stop()
		}
		for name, t := range e.transports {
			if err := t.Stop(); err != nil {
				slog.Error("rollback: failed to stop transport", "name", name, "error", err)
			}
		}
		e.drivers = make(map[string]core.Driver)
		e.transports = make(map[string]core.Transport)
		e.batchers = make(map[string]*transportBatcher)
		e.mu.Unlock()
		if e.dataBus != nil {
			e.dataBus.Close()
		}
		e.mu.Lock()
		e.status = core.EngineStatusStopped
		e.mu.Unlock()
	}()

	// Create data bus and scheduler under the lock so concurrent
	// Stats()/ListDrivers() callers don't race on the pointer writes.
	e.mu.Lock()
	e.dataBus = NewDataBus(e.dataBusSize)
	e.scheduler = NewScheduler(e.readFromDriver, e.onDriverData, e.errorThrottleWin)
	e.mu.Unlock()

	if err := e.scheduler.Start(e.ctx); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}

	// Auto-fill node config: if node.id is set, auto-generate topic-template,
	// command-topic, parser, and forward rules where not explicitly configured.
	e.autoFillNodeConfig(config)

	// Initialize transports first (they're the data consumers)
	for _, tc := range config.Transports {
		if err := e.AddTransport(tc); err != nil {
			slog.Error("failed to add transport", "name", tc.Name, "error", err)
			return fmt.Errorf("failed to add transport %s: %w", tc.Name, err)
		}
	}

	// Set up rule providers
	for _, pc := range config.RuleProviders {
		p, err := rule.NewFileProvider(pc.Name, pc.Path, pc.Interval)
		if err != nil {
			slog.Error("failed to create rule provider", "name", pc.Name, "error", err)
			return fmt.Errorf("failed to create rule provider %s: %w", pc.Name, err)
		}
		e.ruleEngine.AddProvider(p)
	}

	// Set up sub-rule groups
	if len(config.RuleGroups) > 0 {
		if err := e.ruleEngine.SetSubRules(config.RuleGroups); err != nil {
			return fmt.Errorf("failed to set sub-rules: %w", err)
		}
	}

	// Set rules
	if err := e.SetRules(config.Rules); err != nil {
		return fmt.Errorf("failed to set rules: %w", err)
	}

	// Initialize and start drivers
	for _, dc := range config.Drivers {
		if err := e.AddDriver(dc); err != nil {
			slog.Error("failed to add driver", "name", dc.Name, "error", err)
			return fmt.Errorf("failed to add driver %s: %w", dc.Name, err)
		}
	}

	// Start the processing pipeline — N worker goroutines consume
	// from the same DataBus channel, parallelising cache update,
	// rule matching and publishing.
	numWorkers := e.numWorkers
	if numWorkers < 1 {
		numWorkers = 1
	}
	e.wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go e.processingLoop()
	}

	// Start handling write commands from transports
	e.wg.Add(1)
	go e.commandLoop()

	// Start topology auto-discovery if node config is present.
	e.startDiscovery(config)

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

	// Use a shutdown timeout so Stop() can't hang forever (M22).
	stopTimeout := e.shutdownTimeout

	// Stop all drivers with timeout
	for i, d := range drivers {
		done := make(chan error, 1)
		go func() { done <- d.Stop() }()
		select {
		case err := <-done:
			if err != nil {
				slog.Error("failed to stop driver", "name", driverNames[i], "error", err)
			}
		case <-time.After(stopTimeout):
			slog.Error("timed out stopping driver", "name", driverNames[i], "timeout", stopTimeout)
		}
	}

	// Stop all batchers (flush remaining buffered data)
	for i, b := range batchers {
		b.stop()
		slog.Info("batcher stopped", "name", batcherNames[i])
	}

	// Stop all transports with timeout
	for i, t := range transports {
		done := make(chan error, 1)
		go func() { done <- t.Stop() }()
		select {
		case err := <-done:
			if err != nil {
				slog.Error("failed to stop transport", "name", transportNames[i], "error", err)
			}
		case <-time.After(stopTimeout):
			slog.Error("timed out stopping transport", "name", transportNames[i], "timeout", stopTimeout)
		}
	}

	// Close data bus
	if e.dataBus != nil {
		e.dataBus.Close()
	}

	// Close rule providers to stop their reload-loop goroutines.
	// Without this, Reload()→Start() leaks the old providers' goroutines
	// because Start() creates new providers without closing the old ones
	// (problem 4).
	if e.ruleEngine != nil {
		e.ruleEngine.CloseProviders()
	}

	// Stop all tag-file watchers to prevent goroutine leaks on reload.
	e.stopAllTagFileWatchers()

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

func (e *CoreCEngine) Status() core.EngineStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status
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

// --- Driver Management ---

func (e *CoreCEngine) AddDriver(config core.DriverConfig) error {
	driver, err := core.CreateDriver(config)
	if err != nil {
		return fmt.Errorf("failed to create driver %s: %w", config.Name, err)
	}

	if err := driver.Init(e.ctx, config); err != nil {
		return fmt.Errorf("failed to init driver %s: %w", config.Name, err)
	}

	if err := driver.Start(e.ctx); err != nil {
		return fmt.Errorf("failed to start driver %s: %w", config.Name, err)
	}

	e.mu.Lock()
	// If a driver with the same name already exists (e.g. via direct API call),
	// remove it from the map now so concurrent callers don't see a stopping
	// driver. The actual Stop() happens after Unlock to avoid blocking the lock.
	var oldDriver core.Driver
	if d, exists := e.drivers[config.Name]; exists {
		oldDriver = d
		delete(e.drivers, config.Name)
		delete(e.tagGroups, config.Name)
	}
	e.mu.Unlock()

	// Stop the old driver outside the lock — it's already removed from the
	// map, so no concurrent caller can interact with it.
	if oldDriver != nil {
		if e.scheduler != nil {
			if err := e.scheduler.PauseDriver(config.Name); err != nil {
				slog.Error("failed to pause old driver in scheduler", "name", config.Name, "error", err)
			}
		}
		if err := oldDriver.Stop(); err != nil {
			slog.Error("failed to stop old driver on replacement", "name", config.Name, "error", err)
		}
	}

	e.mu.Lock()
	e.drivers[config.Name] = driver

	// Build tag -> group map for DataPoint.Group enrichment in onDriverData.
	// Only tags that declare a group are stored, keeping the map small.
	tagGroups := make(map[string]string, len(config.Tags))
	for _, t := range config.Tags {
		if t.Group != "" {
			tagGroups[t.Name] = t.Group
		}
	}
	e.tagGroups[config.Name] = tagGroups
	e.mu.Unlock()

	// Schedule collection tasks based on tag intervals
	e.scheduleDriverTags(config)

	// Store config for tag-file watcher rebuilds.
	e.mu.Lock()
	e.driverConfigs[config.Name] = config
	e.mu.Unlock()

	// Start tag-file watcher if configured.
	e.startTagFileWatcher(config)

	slog.Info("driver added", "name", config.Name, "type", config.Type, "tags", len(config.Tags))
	return nil
}

func (e *CoreCEngine) RemoveDriver(name string) error {
	// Stop tag-file watcher first so it doesn't try to reload a
	// driver that's being removed.
	e.stopTagFileWatcher(name)

	e.mu.Lock()
	driver, ok := e.drivers[name]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("driver not found: %s", name)
	}
	delete(e.drivers, name)
	delete(e.tagGroups, name)
	delete(e.driverConfigs, name)
	e.mu.Unlock()

	if e.scheduler != nil {
		// Remove all scheduler tasks for this driver so their goroutines
		// actually exit, instead of just pausing them and leaving the
		// goroutines running forever (problem 3).
		e.scheduler.RemoveDriverTasks(name)
	}
	return driver.Stop()
}

func (e *CoreCEngine) GetDriver(name string) (core.Driver, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d, ok := e.drivers[name]
	return d, ok
}

func (e *CoreCEngine) ListDrivers() []core.DriverStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]core.DriverStatus, 0, len(e.drivers))
	for _, d := range e.drivers {
		result = append(result, d.Status())
	}
	return result
}

// ── Tag-File Watcher helpers ────────────────────────────────────────

// startTagFileWatcher starts a hot-reload watcher for a driver's tags-file
// if both TagsFile and TagsInterval are configured.
func (e *CoreCEngine) startTagFileWatcher(config core.DriverConfig) {
	if config.TagsFile == "" || config.TagsInterval == "" {
		return
	}

	// The reload callback replaces the driver's tags with the file content.
	// It rebuilds the DriverConfig with the new tags and re-adds the driver.
	driverName := config.Name
	onReload := func(newTags []core.TagConfig) error {
		e.mu.RLock()
		baseCfg, ok := e.driverConfigs[driverName]
		e.mu.RUnlock()
		if !ok {
			return fmt.Errorf("driver %s config not found during tag-file reload", driverName)
		}

		// Build updated config: new file tags + original inline tags.
		// We need to recover inline tags from the stored config.
		// baseCfg.Tags already includes file tags from initial parse,
		// so we can't easily separate them. Instead, just use newTags
		// as the complete replacement (file is the source of truth).
		updatedCfg := baseCfg
		updatedCfg.Tags = newTags

		slog.Info("reloading driver from tags-file",
			"driver", driverName, "old_tags", len(baseCfg.Tags), "new_tags", len(newTags))

		// Remove and re-add the driver with updated tags.
		if err := e.RemoveDriver(driverName); err != nil {
			return fmt.Errorf("failed to remove driver for tag reload: %w", err)
		}
		// Clear the watcher we just stopped so AddDriver can start a fresh one.
		e.mu.Lock()
		delete(e.tagFileWatchers, driverName)
		e.mu.Unlock()

		if err := e.AddDriver(updatedCfg); err != nil {
			return fmt.Errorf("failed to re-add driver with new tags: %w", err)
		}
		return nil
	}

	w, err := newTagFileWatcher(config.Name, config.TagsFile, config.TagsInterval, onReload)
	if err != nil {
		slog.Error("failed to start tag-file watcher",
			"driver", config.Name, "path", config.TagsFile, "error", err)
		return
	}

	e.mu.Lock()
	e.tagFileWatchers[config.Name] = w
	e.mu.Unlock()
}

// stopTagFileWatcher stops the watcher for a specific driver if one exists.
func (e *CoreCEngine) stopTagFileWatcher(name string) {
	e.mu.Lock()
	w, ok := e.tagFileWatchers[name]
	if ok {
		delete(e.tagFileWatchers, name)
	}
	e.mu.Unlock()
	if ok {
		w.stop()
	}
}

// stopAllTagFileWatchers stops all active tag-file watchers.
func (e *CoreCEngine) stopAllTagFileWatchers() {
	e.mu.Lock()
	watchers := make([]*tagFileWatcher, 0, len(e.tagFileWatchers))
	for name, w := range e.tagFileWatchers {
		watchers = append(watchers, w)
		delete(e.tagFileWatchers, name)
	}
	e.mu.Unlock()
	for _, w := range watchers {
		w.stop()
	}
}

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
	if t, exists := e.transports[config.Name]; exists {
		oldTransport = t
		delete(e.transports, config.Name)
		if b, hasBatcher := e.batchers[config.Name]; hasBatcher {
			oldBatcher = b
			delete(e.batchers, config.Name)
		}
	}
	e.mu.Unlock()

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
	if b := newTransportBatcher(transport, config, e.offlineBuffer); b != nil {
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
	if e.ctx != nil && e.ctx.Err() == nil {
		e.startCommandListener(transport)
		e.startDataListener(transport)
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
	e.mu.Unlock()

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

// --- Rule Management ---

func (e *CoreCEngine) SetRules(rules []core.RuleConfig) error {
	return e.ruleEngine.SetRules(rules)
}

func (e *CoreCEngine) GetRuleStats() []core.RuleStat {
	return e.ruleEngine.RuleStats()
}

func (e *CoreCEngine) SetRuleDisabled(index int, disabled bool) error {
	return e.ruleEngine.SetRuleDisabled(index, disabled)
}

// --- Data Operations ---

func (e *CoreCEngine) ReadTag(ctx context.Context, driver, tag string) (*core.TagValue, error) {
	e.mu.RLock()
	d, ok := e.drivers[driver]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver not found: %s", driver)
	}

	values, err := d.Read(ctx, []string{tag})
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no value returned for tag: %s", tag)
	}
	return &values[0], nil
}

func (e *CoreCEngine) WriteTag(ctx context.Context, cmd core.WriteCommand) (*core.WriteResult, error) {
	e.mu.RLock()
	d, ok := e.drivers[cmd.Driver]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver not found: %s", cmd.Driver)
	}

	results, err := d.Write(ctx, []core.WriteCommand{cmd})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no result returned")
	}
	return &results[0], nil
}

func (e *CoreCEngine) LatestValues(driver string) map[string]core.DataPoint {
	return e.cache.GetByDriver(driver)
}

func (e *CoreCEngine) StaleThreshold() time.Duration {
	return e.staleThreshold
}

// --- Event Subscription ---

func (e *CoreCEngine) Subscribe(filter string) (ch <-chan core.DataPoint, unsub func()) {
	return e.dataBus.Subscribe(filter)
}

// SubscribeWithBuffer is like Subscribe but lets the caller choose the
// subscriber channel buffer size.
func (e *CoreCEngine) SubscribeWithBuffer(filter string, size int) (ch <-chan core.DataPoint, unsub func()) {
	return e.dataBus.SubscribeWithBuffer(filter, size)
}

func (e *CoreCEngine) OnAlert(handler func(point core.DataPoint, rule core.Rule)) {
	e.mu.Lock()
	e.alertHandlers = append(e.alertHandlers, handler)
	e.mu.Unlock()
}

// --- Stats ---

func (e *CoreCEngine) Stats() core.EngineStats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	driverStats := make(map[string]core.DriverStatus)
	for name, d := range e.drivers {
		driverStats[name] = d.Status()
	}

	transportStats := make(map[string]core.TransportStatus)
	for name, t := range e.transports {
		transportStats[name] = t.Status()
	}

	uptime := time.Since(e.startTime)
	totalRead := e.totalRead.Load()
	pointsPerSec := float64(0)
	if uptime.Seconds() > 0 {
		pointsPerSec = float64(totalRead) / uptime.Seconds()
	}

	return core.EngineStats{
		Status:         e.status,
		Uptime:         uptime,
		Drivers:        len(e.drivers),
		Transports:     len(e.transports),
		Rules:          len(e.ruleEngine.Rules()),
		TotalRead:      totalRead,
		TotalPublish:   e.totalPublish.Load(),
		TotalErrors:    e.totalErrors.Load(),
		TotalDropped:   uint64(e.dataBus.Dropped() + e.dataBus.PushDropped() + log.Dropped()),
		PointsPerSec:   pointsPerSec,
		DriverStats:    driverStats,
		TransportStats: transportStats,
	}
}

// --- Internal Methods ---

func (e *CoreCEngine) readFromDriver(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
	e.mu.RLock()
	d, ok := e.drivers[driver]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver not found: %s", driver)
	}
	return d.Read(ctx, tags)
}

func (e *CoreCEngine) onDriverData(driver string, values []core.TagValue) {
	// Enrich DataPoint.Group from the TagConfig.Group captured in AddDriver.
	// TagValue only carries the tag name, so the group must be looked up by
	// (driver, tag). We read the per-driver map once under RLock to avoid
	// per-point lock churn.
	//
	// NOTE on Device: there is currently no Device source in config
	// (DriverConfig has no Device field and TagConfig has no Device field),
	// so DataPoint.Device is left empty here. To populate it, add a Device
	// field to DriverConfig (driver-wide) or TagConfig (per-tag) and store
	// it alongside tagGroups, then set point.Device below.
	e.mu.RLock()
	groups := e.tagGroups[driver]
	e.mu.RUnlock()

	for _, v := range values {
		e.totalRead.Add(1)

		point := core.DataPoint{
			Driver:    driver,
			Tag:       v.Tag,
			Value:     v.Value,
			Type:      v.Type,
			Quality:   v.Quality,
			Timestamp: v.Timestamp,
		}
		if groups != nil {
			point.Group = groups[v.Tag]
		}

		e.dataBus.Push(point)
	}
}

func (e *CoreCEngine) scheduleDriverTags(config core.DriverConfig) {
	// Group tags by interval
	intervalGroups := make(map[string][]string)
	deadBands := make(map[string]float64)
	// readTimeouts tracks the max parsed read-timeout per interval group.
	// Tags in the same group are read together in a single readFunc call,
	// so the effective timeout is the most generous one requested.
	readTimeouts := make(map[string]time.Duration)
	for _, tag := range config.Tags {
		interval := tag.Interval
		if interval == "" {
			interval = e.defaultTagInterval.String() // default
		}
		intervalGroups[interval] = append(intervalGroups[interval], tag.Name)
		if tag.DeadBand > 0 {
			deadBands[tag.Name] = tag.DeadBand
		}
		if tag.ReadTimeout != "" {
			if d, err := time.ParseDuration(tag.ReadTimeout); err == nil && d > 0 {
				if d > readTimeouts[interval] {
					readTimeouts[interval] = d
				}
			}
		}
	}

	for intervalStr, tags := range intervalGroups {
		interval, err := time.ParseDuration(intervalStr)
		if err != nil {
			slog.Error("invalid interval", "interval", intervalStr, "error", err)
			interval = e.defaultTagInterval
		}

		taskID := fmt.Sprintf("%s_%s", config.Name, intervalStr)
		if err := e.scheduler.AddTask(core.ScheduleTask{
			ID:          taskID,
			Driver:      config.Name,
			Tags:        tags,
			Interval:    interval,
			DeadBands:   deadBands,
			ReadTimeout: readTimeouts[intervalStr],
		}); err != nil {
			slog.Error("failed to add schedule task", "task", taskID, "error", err)
		}
	}
}

func (e *CoreCEngine) processingLoop() {
	defer e.wg.Done()

	for {
		select {
		case <-e.ctx.Done():
			return
		case point, ok := <-e.dataBus.Channel():
			if !ok {
				return
			}

			// Apply bad-quality policy before any further processing.
			// This prevents downstream systems from receiving
			// misleading bad-quality values unless explicitly configured.
			if point.Quality == core.QualityBad {
				switch e.badQualityPolicy {
				case badQualityDrop:
					e.totalErrors.Add(1)
					statistic.DefaultManager.PushError()
					continue // discard entirely
				case badQualityMarkAndPublish:
					point.Value = nil // keep quality=bad, clear value
				case badQualityAlert:
					// Forward + alert — handled below via fireAlert
				}
			}

			// Update cache
			e.cache.Update(point)

			// Broadcast to subscribers
			e.dataBus.Broadcast(point)

			// Match rules
			result := e.ruleEngine.Match(point)
			if result == nil {
				continue
			}

			// Execute action
			alerted := false
			switch result.Rule.Action() {
			case core.ActionDrop:
				continue
			case core.ActionAlert:
				e.fireAlert(point, result.Rule)
				alerted = true
				e.publishToTargets(point, result.Targets)
			case core.ActionTransform:
				e.publishToTargets(e.applyTransform(point, result.Transform), result.Targets)
			case core.ActionForward, core.ActionMirror:
				e.publishToTargets(point, result.Targets)
			}

			// Bad-quality alert: fire alert in addition to the normal action,
			// but skip if the rule action already fired an alert above.
			if point.Quality == core.QualityBad && e.badQualityPolicy == badQualityAlert && !alerted {
				e.fireAlert(point, result.Rule)
			}
		}
	}
}

func (e *CoreCEngine) commandLoop() {
	defer e.wg.Done()

	// Listener goroutines for transports are started by AddTransport,
	// which is called for every transport during Start() and for any
	// transport added later via the management API.  This goroutine
	// exists solely to be tracked by e.wg so that Stop() waits for
	// all listener goroutines to exit before returning.
	<-e.ctx.Done()
}

// startCommandListener launches a goroutine that listens for write commands
// from a transport's OnCommand channel and dispatches them to the engine.
// Failed writes are retried with exponential backoff; if all retries fail,
// the command is placed in the dead letter queue for later inspection.
// Called both from commandLoop (for initial transports) and from AddTransport
// (for transports added at runtime), so no transport's commands are dropped.
func (e *CoreCEngine) startCommandListener(t core.Transport) {
	cmdCh := t.OnCommand()
	if cmdCh == nil {
		return
	}
	e.wg.Add(1)
	go func(ch <-chan core.WriteCommand) {
		defer e.wg.Done()
		for {
			select {
			case <-e.ctx.Done():
				return
			case cmd, ok := <-ch:
				if !ok {
					return
				}
				e.executeWriteWithRetry(cmd)
			}
		}
	}(cmdCh)
}

// executeWriteWithRetry attempts a write command up to writeRetryCount+1
// times with exponential backoff. On permanent failure, the command is
// added to the dead letter queue.
func (e *CoreCEngine) executeWriteWithRetry(cmd core.WriteCommand) {
	maxAttempts := e.writeRetryCount + 1
	var lastErr string

	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, err := e.WriteTag(e.ctx, cmd)
		switch {
		case err != nil:
			lastErr = err.Error()
		case !result.Success:
			lastErr = result.Error
		default:
			slog.Info("command write success", "driver", cmd.Driver, "tag", cmd.Tag, "attempt", attempt+1)
			return
		}

		if attempt < maxAttempts-1 {
			delay := time.Duration(100*(1<<attempt)) * time.Millisecond // 100ms, 200ms, 400ms...
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
			slog.Warn("command write retry",
				"driver", cmd.Driver, "tag", cmd.Tag,
				"attempt", attempt+1, "max", maxAttempts,
				"delay", delay, "error", lastErr)
			select {
			case <-e.ctx.Done():
				slog.Warn("command write retry cancelled by shutdown",
					"driver", cmd.Driver, "tag", cmd.Tag,
					"attempt", attempt+1, "error", lastErr)
				return
			case <-time.After(delay):
			}
		}
	}

	// All retries exhausted — add to dead letter queue.
	slog.Error("command write failed after retries, added to dead letter queue",
		"driver", cmd.Driver, "tag", cmd.Tag, "attempts", maxAttempts, "error", lastErr)
	e.addDeadLetter(core.DeadLetterEntry{
		Command:  cmd,
		Error:    lastErr,
		FailedAt: time.Now(),
		Attempts: maxAttempts,
	})
}

// addDeadLetter appends a failed write command to the dead letter queue,
// evicting the oldest entry if the queue is full.
func (e *CoreCEngine) addDeadLetter(entry core.DeadLetterEntry) {
	e.deadLetterMu.Lock()
	defer e.deadLetterMu.Unlock()
	e.deadLetterQueue = append(e.deadLetterQueue, entry)
	if len(e.deadLetterQueue) > e.deadLetterMaxLen {
		e.deadLetterQueue = e.deadLetterQueue[len(e.deadLetterQueue)-e.deadLetterMaxLen:]
	}
}

// DeadLetterEntries returns a copy of the current dead letter queue.
func (e *CoreCEngine) DeadLetterEntries() []core.DeadLetterEntry {
	e.deadLetterMu.Lock()
	defer e.deadLetterMu.Unlock()
	result := make([]core.DeadLetterEntry, len(e.deadLetterQueue))
	copy(result, e.deadLetterQueue)
	return result
}

// startDataListener launches a goroutine that listens for data points from
// a transport's OnData channel and feeds them into the DataBus — the
// chained-core inbound path.  DataPoints are piped back to the
// processing loop via a channel so chained transports feed the same
// pipeline as driver-sourced data.
//
// Called from commandLoop (initial transports) and AddTransport (runtime).
func (e *CoreCEngine) startDataListener(t core.Transport) {
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
			case <-e.ctx.Done():
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
			if err := entry.batcher.publish(e.ctx, point); err != nil {
				slog.Error("batch publish failed", "target", entry.name, "error", err)
				e.totalErrors.Add(1)
				statistic.DefaultManager.PushError()
				e.tryFallback(point, entry.name, fallbackSnapshot)
			} else {
				e.totalPublish.Add(1)
				statistic.DefaultManager.PushPublish(1)
			}
			continue
		}

		if err := entry.transport.Publish(e.ctx, point); err != nil {
			slog.Error("publish failed", "target", entry.name, "error", err)
			e.totalErrors.Add(1)
			statistic.DefaultManager.PushError()
			e.tryFallback(point, entry.name, fallbackSnapshot)
		} else {
			e.totalPublish.Add(1)
			statistic.DefaultManager.PushPublish(1)
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
		if err := fb.batcher.publish(e.ctx, point); err != nil {
			slog.Error("fallback batch publish also failed", "fallback", fb.name, "error", err)
			e.totalErrors.Add(1)
			statistic.DefaultManager.PushError()
		} else {
			e.totalPublish.Add(1)
			statistic.DefaultManager.PushPublish(1)
		}
		return
	}

	if err := fb.transport.Publish(e.ctx, point); err != nil {
		slog.Error("fallback publish also failed", "fallback", fb.name, "error", err)
		e.totalErrors.Add(1)
		statistic.DefaultManager.PushError()
	} else {
		e.totalPublish.Add(1)
		statistic.DefaultManager.PushPublish(1)
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
func (e *CoreCEngine) autoFillNodeConfig(config *core.Config) {
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

// startDiscovery launches the topology auto-discovery module if node config
// is present. It collects the broker URLs from MQTT transports and derives
// the publish endpoint (subscription pattern) from each transport's topic-template.
func (e *CoreCEngine) startDiscovery(config *core.Config) {
	if config.Node.ID == "" {
		return // auto-discovery disabled
	}

	// Collect broker endpoints from MQTT transports.
	brokerMap := make(map[string]*brokerEndpoint) // deduplicate by broker URL
	for _, tc := range config.Transports {
		if tc.Type != "mqtt" {
			continue
		}
		broker, ok := tc.Settings["broker"].(string)
		if !ok || broker == "" {
			continue
		}

		be, exists := brokerMap[broker]
		if !exists {
			be = &brokerEndpoint{Broker: broker}
			brokerMap[broker] = be
		}

		// If this transport has a topic-template (explicit or auto-filled),
		// it's an outbound transport — advertise a publish endpoint.
		if topicTpl, ok := tc.Settings["topic-template"].(string); ok && topicTpl != "" {
			be.Publish = &endpoint{
				Type:  "mqtt",
				Topic: topicTemplateToSubscriptionPattern(topicTpl),
			}
		}

		// HTTP receive endpoint discovery (webhook-addr) is deferred to a
		// future iteration. When implemented, it will be collected from
		// HTTP transports in a separate loop and paired with the broker
		// used for discovery heartbeats.
	}

	if len(brokerMap) == 0 {
		slog.Warn("discovery: node config set but no MQTT transports found, auto-discovery disabled",
			"node", config.Node.ID)
		return
	}

	brokers := make([]brokerEndpoint, 0, len(brokerMap))
	for _, be := range brokerMap {
		brokers = append(brokers, *be)
	}

	e.discovery = NewDiscovery(config.Node, brokers, e.AddTransport)
	if err := e.discovery.Start(e.ctx); err != nil {
		slog.Error("failed to start discovery, continuing without auto-discovery", "error", err)
		e.discovery = nil
	}
}
