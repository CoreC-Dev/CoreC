package engine

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
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
	drivers    map[string]core.Driver
	transports map[string]core.Transport
	batchers   map[string]*transportBatcher
	ruleEngine *rule.Engine
	scheduler  *scheduler
	dataBus    *DataBus
	cache      *LatestCache

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

	// Lifecycle
	ctx       context.Context
	parentCtx context.Context // saved from Start() for Reload()
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// New creates a new CoreC Engine instance.
func New() core.Engine {
	return &CoreCEngine{
		status:             core.EngineStatusStopped,
		drivers:            make(map[string]core.Driver),
		transports:         make(map[string]core.Transport),
		batchers:           make(map[string]*transportBatcher),
		tagGroups:          make(map[string]map[string]string),
		ruleEngine:         rule.NewEngine(),
		cache:              NewLatestCache(),
		numWorkers:         runtime.NumCPU(),
		dataBusSize:        core.DefaultDataBusSize,
		shutdownTimeout:    core.DefaultShutdownTimeout,
		errorThrottleWin:   core.DefaultErrorThrottleWindow,
		defaultTagInterval: core.DefaultTagInterval,
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

	started = true
	slog.Info("CoreC engine started successfully")
	return nil
}

func (e *CoreCEngine) Stop() error {
	slog.Info("CoreC engine stopping")

	if e.cancel != nil {
		e.cancel()
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

	slog.Info("driver added", "name", config.Name, "type", config.Type, "tags", len(config.Tags))
	return nil
}

func (e *CoreCEngine) RemoveDriver(name string) error {
	e.mu.Lock()
	driver, ok := e.drivers[name]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("driver not found: %s", name)
	}
	delete(e.drivers, name)
	delete(e.tagGroups, name)
	e.mu.Unlock()

	if e.scheduler != nil {
		if err := e.scheduler.PauseDriver(name); err != nil {
			slog.Error("failed to pause driver in scheduler", "name", name, "error", err)
		}
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

	// Create batcher if batch/flush/retry config is set
	if b := newTransportBatcher(transport, config); b != nil {
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
	e.mu.Unlock()
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
	for _, tag := range config.Tags {
		interval := tag.Interval
		if interval == "" {
			interval = e.defaultTagInterval.String() // default
		}
		intervalGroups[interval] = append(intervalGroups[interval], tag.Name)
		if tag.DeadBand > 0 {
			deadBands[tag.Name] = tag.DeadBand
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
			ID:        taskID,
			Driver:    config.Name,
			Tags:      tags,
			Interval:  interval,
			DeadBands: deadBands,
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
			switch result.Rule.Action() {
			case core.ActionDrop:
				continue
			case core.ActionAlert:
				e.fireAlert(point, result.Rule)
				e.publishToTargets(point, result.Targets)
			case core.ActionTransform:
				e.publishToTargets(e.applyTransform(point, result.Transform), result.Targets)
			case core.ActionForward, core.ActionMirror:
				e.publishToTargets(point, result.Targets)
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
				result, err := e.WriteTag(e.ctx, cmd)
				switch {
				case err != nil:
					slog.Error("command write failed", "driver", cmd.Driver, "tag", cmd.Tag, "error", err)
				case !result.Success:
					slog.Error("command write failed", "driver", cmd.Driver, "tag", cmd.Tag, "error", result.Error)
				default:
					slog.Info("command write success", "driver", cmd.Driver, "tag", cmd.Tag)
				}
			}
		}
	}(cmdCh)
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

func (e *CoreCEngine) publishToTargets(point core.DataPoint, targets []string) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, targetName := range targets {
		transport, ok := e.transports[targetName]
		if !ok {
			slog.Warn("target transport not found", "target", targetName)
			e.totalErrors.Add(1)
			continue
		}

		// Use batcher if configured, otherwise publish directly
		if batcher, ok := e.batchers[targetName]; ok {
			if err := batcher.publish(e.ctx, point); err != nil {
				slog.Error("batch publish failed", "target", targetName, "error", err)
				e.totalErrors.Add(1)
				statistic.DefaultManager.PushError()
			} else {
				e.totalPublish.Add(1)
				statistic.DefaultManager.PushPublish(1)
			}
			continue
		}

		if err := transport.Publish(e.ctx, point); err != nil {
			slog.Error("publish failed", "target", targetName, "error", err)
			e.totalErrors.Add(1)
			statistic.DefaultManager.PushError()
		} else {
			e.totalPublish.Add(1)
			statistic.DefaultManager.PushPublish(1)
		}
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
