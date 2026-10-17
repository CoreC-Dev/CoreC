package engine

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
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
	ruleSet          *ruleRole
	reads            *readAccessor
	stats            *statsCollector
	driverReg        *driverRegistry
	scheduler        *scheduler
	dataBus          *DataBus
	cache            *LatestCache

	// Alert handlers
	alertHandlers []func(point core.DataPoint, rule core.Rule)

	// Processing parallelism
	numWorkers int

	// Tunable parameters (set from GlobalConfig.Engine in Start)
	dataBusSize        int
	shutdownTimeout    time.Duration
	errorThrottleWin   time.Duration
	defaultTagInterval time.Duration
	badQualityPolicy   badQualityPolicy
	writeRetryCount    int

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

	// offlineBuffer persists failed publish batches to disk for replay
	// after transport recovery. nil = disabled (no buffer config).
	offlineBuffer *OfflineBuffer

	// discovery handles topology auto-discovery via MQTT heartbeats.
	// nil when node config is not set (auto-discovery disabled).
	discovery *Discovery

	// derivedEvaluator periodically computes derived tags from the latest
	// cache and pushes results into the data bus. nil when no derived tags
	// are configured.
	derivedEvaluator *derivedTagEvaluator

	// deadLetterQueue holds write commands that failed after all retries.
	// Bounded by deadLetterMaxLen; oldest entries are evicted when full.
	deadLetterMu     sync.Mutex
	deadLetterQueue  []core.DeadLetterEntry
	deadLetterMaxLen int

	// writeAuditHistory records the most recent write attempts —
	// successes AND failures — for the GET /write/history audit trail.
	// Bounded by writeAuditCapacity; oldest entries are evicted when
	// full. Guarded by its own small mutex so audit recording never
	// contends with the engine-wide e.mu.
	writeAuditMu      sync.Mutex
	writeAuditHistory []core.WriteAuditEntry

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

func parseBadQualityPolicy(s string) (badQualityPolicy, error) {
	switch strings.ToLower(s) {
	case "", "publish":
		return badQualityPublish, nil
	case "drop":
		return badQualityDrop, nil
	case "mark-and-publish":
		return badQualityMarkAndPublish, nil
	case "alert":
		return badQualityAlert, nil
	default:
		return badQualityPublish, fmt.Errorf("invalid on-bad-quality %q (want publish|drop|mark-and-publish|alert)", s)
	}
}

// newEngineShell allocates the engine composition: the CoreCEngine with
// every role struct created (zero-valued) and wired. New() applies the
// production defaults on top; tests use it to build bare engines with
// partial state instead of a struct literal (the roles must never be
// nil, or the forwarders would dereference them).
func newEngineShell() *CoreCEngine {
	e := &CoreCEngine{
		ruleSet:   &ruleRole{},
		reads:     &readAccessor{},
		stats:     &statsCollector{},
		driverReg: &driverRegistry{},
	}
	e.reads.eng = e
	e.stats.eng = e
	e.driverReg.eng = e
	return e
}

// New creates a new CoreC Engine instance.
func New() core.Engine {
	e := newEngineShell()
	e.status = core.EngineStatusStopped
	e.driverReg.drivers = make(map[string]core.Driver)
	e.transports = make(map[string]core.Transport)
	e.transportCfg = make(map[string]core.TransportConfig)
	e.transportCancels = make(map[string]context.CancelFunc)
	e.batchers = make(map[string]*transportBatcher)
	e.driverReg.tagGroups = make(map[string]map[string]string)
	e.ruleSet.ruleEngine = rule.NewEngine()
	e.cache = NewLatestCache()
	e.numWorkers = runtime.NumCPU()
	e.dataBusSize = core.DefaultDataBusSize
	e.shutdownTimeout = core.DefaultShutdownTimeout
	e.errorThrottleWin = core.DefaultErrorThrottleWindow
	e.defaultTagInterval = core.DefaultTagInterval
	e.badQualityPolicy = badQualityPublish
	e.writeRetryCount = 3
	e.commandConcurrency = core.DefaultCommandConcurrency
	e.numHighPriorityWorkers = core.DefaultHighPriorityWorkers
	e.stats.statManager = statistic.NewManager()
	e.deadLetterMaxLen = core.DefaultDeadLetterMaxLen
	e.driverReg.tagFileWatchers = make(map[string]*tagFileWatcher)
	e.driverReg.driverConfigs = make(map[string]core.DriverConfig)
	e.stats.readLatency = metrics.NewLatencyHistogram(metrics.DefaultReadLatencyBuckets)
	e.stats.publishLatency = metrics.NewLatencyHistogram(metrics.DefaultPublishLatencyBuckets)
	e.stats.dataAge = metrics.NewLatencyHistogram(metrics.DefaultDataAgeBuckets)
	return e
}
