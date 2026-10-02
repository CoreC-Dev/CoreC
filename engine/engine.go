package engine

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/metrics"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine/statistic"
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
