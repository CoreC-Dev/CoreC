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

	"github.com/CoreC-Dev/CoreC/common/metrics"
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

	// transportCancels holds the per-transport context cancel func for each
	// transport's startCommandListener/startDataListener goroutines. Each
	// listener selects on a child context derived from e.ctx instead of e.ctx
	// directly, so RemoveTransport can stop just that transport's listeners
	// without cancelling the whole engine. This fixes the goroutine leak
	// where MQTT transports' listener goroutines blocked forever after
	// RemoveTransport (MQTTTransport.Stop deliberately does not close its
	// command/data channels).
	transportCancels map[string]context.CancelFunc
	ruleEngine       core.RuleEngine
	scheduler        *scheduler
	dataBus          *DataBus
	cache            *LatestCache

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

	// Latency histograms for Prometheus exposure. Initialized in New()
	// and accumulated across the engine lifetime (mirroring the total*
	// counters above, which are also not reset on Reload). Exposed via
	// ReadLatency()/PublishLatency() for the route metrics handler.
	readLatency    *metrics.LatencyHistogram
	publishLatency *metrics.LatencyHistogram
	// dataAge records the freshness of data at publish time: elapsed time
	// since DataPoint.Timestamp (collection/source time) to the publish
	// attempt. Larger than I/O latency because it includes scheduler
	// queuing, batching, and offline-buffer replay during outages.
	dataAge *metrics.LatencyHistogram

	// Processing parallelism
	numWorkers int

	// Tunable parameters (set from GlobalConfig.Engine in Start)
	dataBusSize        int
	shutdownTimeout    time.Duration
	errorThrottleWin   time.Duration
	defaultTagInterval time.Duration
	badQualityPolicy   badQualityPolicy
	// staleThreshold is stored as nanoseconds in an atomic so that
	// StaleThreshold() can be read lock-free while applyEngineConfig
	// writes it during Reload/Start without a data race.
	staleThreshold  atomic.Int64 // nanoseconds; 0 = disabled
	writeRetryCount int

	// commandConcurrency caps the number of write commands executed in
	// parallel. commandSem is a counting semaphore (buffered channel)
	// created in Start() after applyEngineConfig. Each command acquires a
	// slot before dispatch; when full, the listener applies backpressure
	// (the transport's command channel fills, overflow → dead-letter #4).
	// This prevents a single slow write from serializing all subsequent
	// control commands (IMPROVEMENTS #2). nil before Start() → synchronous.
	commandConcurrency int
	commandSem         chan struct{}

	// numHighPriorityWorkers is the number of dedicated workers that
	// process only high-priority (fast-interval) data from the DataBus
	// high-priority channel. This isolates high-frequency collection from
	// low-frequency bulk-read bursts (IMPROVEMENTS #5).
	numHighPriorityWorkers int

	// statManager collects throughput counters (reads/publishes/errors).
	// It is an instance field rather than the package-level
	// statistic.DefaultManager so that multiple Engine instances embedded
	// in the same process do not mix their counters (IMPROVEMENTS #8).
	// Injected into the scheduler via NewScheduler.
	statManager *statistic.Manager

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
	badQualityPublish        badQualityPolicy = iota // forward normally (default)
	badQualityDrop                                   // discard before rule matching
	badQualityMarkAndPublish                         // set Value=nil, keep quality=bad, forward
	badQualityAlert                                  // forward + trigger alert callback
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
		status:                 core.EngineStatusStopped,
		drivers:                make(map[string]core.Driver),
		transports:             make(map[string]core.Transport),
		transportCfg:           make(map[string]core.TransportConfig),
		transportCancels:       make(map[string]context.CancelFunc),
		batchers:               make(map[string]*transportBatcher),
		tagGroups:              make(map[string]map[string]string),
		ruleEngine:             rule.NewEngine(),
		cache:                  NewLatestCache(),
		numWorkers:             runtime.NumCPU(),
		dataBusSize:            core.DefaultDataBusSize,
		shutdownTimeout:        core.DefaultShutdownTimeout,
		errorThrottleWin:       core.DefaultErrorThrottleWindow,
		defaultTagInterval:     core.DefaultTagInterval,
		badQualityPolicy:       badQualityPublish,
		writeRetryCount:        3,
		commandConcurrency:     core.DefaultCommandConcurrency,
		numHighPriorityWorkers: core.DefaultHighPriorityWorkers,
		statManager:            statistic.NewManager(),
		deadLetterMaxLen:       core.DefaultDeadLetterMaxLen,
		tagFileWatchers:        make(map[string]*tagFileWatcher),
		driverConfigs:          make(map[string]core.DriverConfig),
		readLatency:            metrics.NewLatencyHistogram(metrics.DefaultReadLatencyBuckets),
		publishLatency:         metrics.NewLatencyHistogram(metrics.DefaultPublishLatencyBuckets),
		dataAge:                metrics.NewLatencyHistogram(metrics.DefaultDataAgeBuckets),
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
		e.staleThreshold.Store(int64(d))
	}
	if cfg.WriteRetryCount > 0 {
		e.writeRetryCount = cfg.WriteRetryCount
	}
	if cfg.CommandConcurrency > 0 {
		e.commandConcurrency = cfg.CommandConcurrency
	}
	if cfg.HighPriorityWorkers > 0 {
		e.numHighPriorityWorkers = cfg.HighPriorityWorkers
	}
}

func (e *CoreCEngine) Start(ctx context.Context, config *core.Config) error { //nolint:gocyclo // engine startup orchestrates many subsystems; complexity 26. Refactor tracked as tech debt.
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
	e.scheduler = NewScheduler(e.readFromDriver, e.onDriverData, e.errorThrottleWin, e.statManager)
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

	// Start dedicated high-priority workers that read only from the
	// DataBus high-priority channel. This isolates fast-interval
	// collection from low-frequency bulk-read bursts (IMPROVEMENTS #5).
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

// --- Rule Management ---

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

// --- Stats ---

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

	// dataBus is only created in Start(); before Start it is nil.
	// Report zero dropped counts instead of panicking on a nil receiver.
	var totalDropped uint64
	if e.dataBus != nil {
		totalDropped = uint64(e.dataBus.Dropped() + e.dataBus.PushDropped() + e.dataBus.HighPriorityPushDropped() + log.Dropped())
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
		TotalDropped:   totalDropped,
		PointsPerSec:   pointsPerSec,
		DriverStats:    driverStats,
		TransportStats: transportStats,
	}
}

// OfflineBufferStats aggregates offline-buffer observability counters across
// all transport batchers for Prometheus exposure:
//   - pending: batches currently waiting on disk for replay (gauge).
//   - drained: total batches successfully replayed since startup (counter).
//   - pushed:  total batches persisted to the offline buffer since startup (counter).
//
// The offline buffer is a single shared instance (e.offlineBuffer) handed to
// every batcher in newTransportBatcher, so pending and drained are read once
// from it rather than summed per batcher — summing the per-batcher
// OfflineBufferPending/OfflineBufferDrained values would multiply the real
// count by the number of batchers, since they all delegate to the same
// buffer. pushed, by contrast, is a genuine per-batcher counter
// (offlineBufferPushes, incremented in publishWithRetryAndBuffer) and must be
// summed across batchers.
//
// The batchers map is snapshotted under RLock and then released before
// reading the counters, mirroring the Stop() pattern, so a slow Len() (disk
// readdir) can never block management API calls that take the write lock.
func (e *CoreCEngine) OfflineBufferStats() (pending int, drained, pushed uint64) {
	e.mu.RLock()
	batchers := make([]*transportBatcher, 0, len(e.batchers))
	for _, b := range e.batchers {
		batchers = append(batchers, b)
	}
	buf := e.offlineBuffer
	e.mu.RUnlock()

	// pushed is per-batcher: aggregate it.
	for _, b := range batchers {
		pushed += b.offlineBufferPushes.Load()
	}

	// pending and drained are properties of the shared buffer; read them
	// once to avoid double-counting across batchers.
	if buf != nil {
		pending = buf.PendingCount()
		drained = buf.DrainCount()
	}
	return pending, drained, pushed
}

// FlushBatchesDropped returns the total number of full batches that were
// evicted or dropped from batcher flush queues because the async flush
// consumer fell behind a sustained burst (IMPROVEMENTS #1: async batcher
// backpressure observability). It aggregates the per-batcher
// flushBatchesDropped counters. Intended for Prometheus exposure as
// corec_flush_batches_dropped_total.
func (e *CoreCEngine) FlushBatchesDropped() uint64 {
	e.mu.RLock()
	batchers := make([]*transportBatcher, 0, len(e.batchers))
	for _, b := range e.batchers {
		batchers = append(batchers, b)
	}
	e.mu.RUnlock()

	var total uint64
	for _, b := range batchers {
		total += b.flushBatchesDropped.Load()
	}
	return total
}

// onBatchPublish is the callback handed to each transportBatcher via
// newTransportBatcher. It is called with the number of points actually
// published when publishWithRetryAndBuffer or drainOnce succeeds — not when
// points are merely enqueued for async flush. This keeps totalPublish and
// the statistic manager's publish counter accurate for batched transports,
// where the real PublishBatch happens asynchronously after publish()
// returns (Finding 4 fix: previously publish.go counted totalPublish and
// statManager.PushPublish immediately after the near-instant enqueue, which
// inflated the publish counter and undercounted errors when batches later
// failed after retries).
func (e *CoreCEngine) onBatchPublish(n int) {
	e.totalPublish.Add(uint64(n))
	e.statManager.PushPublish(int64(n))
}

// ReadLatency returns the histogram tracking driver read latency
// (time spent in Driver.Read), for Prometheus histogram exposure. The
// returned pointer is always non-nil for an engine created via New().
func (e *CoreCEngine) ReadLatency() *metrics.LatencyHistogram {
	return e.readLatency
}

// ReadLatencyHistogram returns a point-in-time snapshot of the read
// latency histogram, satisfying the core.LatencyProvider interface.
func (e *CoreCEngine) ReadLatencyHistogram() core.LatencySnapshot {
	buckets, counts, sum, count := e.readLatency.Snapshot()
	return core.LatencySnapshot{Buckets: buckets, Counts: counts, Sum: sum, Count: count}
}

// PublishLatency returns the histogram tracking transport publish
// latency (time spent in Transport.Publish / batcher.publish, including
// fallback attempts), for Prometheus histogram exposure. The returned
// pointer is always non-nil for an engine created via New().
func (e *CoreCEngine) PublishLatency() *metrics.LatencyHistogram {
	return e.publishLatency
}

// PublishLatencyHistogram returns a point-in-time snapshot of the publish
// latency histogram, satisfying the core.LatencyProvider interface.
func (e *CoreCEngine) PublishLatencyHistogram() core.LatencySnapshot {
	buckets, counts, sum, count := e.publishLatency.Snapshot()
	return core.LatencySnapshot{Buckets: buckets, Counts: counts, Sum: sum, Count: count}
}

// DataAgeHistogram returns a point-in-time snapshot of the data-age
// (freshness) histogram, satisfying the core.DataAgeProvider interface.
// Data age is the elapsed time from DataPoint.Timestamp (collection/source
// time) to the publish attempt, measured at each publish site.
func (e *CoreCEngine) DataAgeHistogram() core.LatencySnapshot {
	buckets, counts, sum, count := e.dataAge.Snapshot()
	return core.LatencySnapshot{Buckets: buckets, Counts: counts, Sum: sum, Count: count}
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
