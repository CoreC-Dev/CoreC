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

	ruleSet      *ruleRole
	reads        *readAccessor
	stats        *statsCollector
	driverReg    *driverRegistry
	transportMgr *transportManager
	writes       *writeManager
	processing   *processingCore
	scheduler    *scheduler

	// Tunable parameters (set from GlobalConfig.Engine in Start)
	shutdownTimeout    time.Duration
	errorThrottleWin   time.Duration
	defaultTagInterval time.Duration

	// discovery handles topology auto-discovery via MQTT heartbeats.
	// nil when node config is not set (auto-discovery disabled).
	discovery *Discovery

	// derivedEvaluator periodically computes derived tags from the latest
	// cache and pushes results into the data bus. nil when no derived tags
	// are configured.
	derivedEvaluator *derivedTagEvaluator

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
		ruleSet:      &ruleRole{},
		reads:        &readAccessor{},
		stats:        &statsCollector{},
		driverReg:    &driverRegistry{},
		transportMgr: &transportManager{},
		writes:       &writeManager{},
		processing:   &processingCore{},
	}
	e.reads.eng = e
	e.stats.eng = e
	e.driverReg.eng = e
	e.transportMgr.eng = e
	e.writes.eng = e
	e.processing.eng = e
	return e
}

// New creates a new CoreC Engine instance.
func New() core.Engine {
	e := newEngineShell()
	e.status = core.EngineStatusStopped
	e.driverReg.drivers = make(map[string]core.Driver)
	e.transportMgr.transports = make(map[string]core.Transport)
	e.transportMgr.transportCfg = make(map[string]core.TransportConfig)
	e.transportMgr.transportCancels = make(map[string]context.CancelFunc)
	e.transportMgr.batchers = make(map[string]*transportBatcher)
	e.driverReg.tagGroups = make(map[string]map[string]string)
	e.ruleSet.ruleEngine = rule.NewEngine()
	e.processing.cache = NewLatestCache()
	e.processing.numWorkers = runtime.NumCPU()
	e.processing.dataBusSize = core.DefaultDataBusSize
	e.shutdownTimeout = core.DefaultShutdownTimeout
	e.errorThrottleWin = core.DefaultErrorThrottleWindow
	e.defaultTagInterval = core.DefaultTagInterval
	e.processing.badQualityPolicy = badQualityPublish
	e.writes.writeRetryCount = 3
	e.writes.commandConcurrency = core.DefaultCommandConcurrency
	e.processing.numHighPriorityWorkers = core.DefaultHighPriorityWorkers
	e.stats.statManager = statistic.NewManager()
	e.writes.deadLetterMaxLen = core.DefaultDeadLetterMaxLen
	e.driverReg.tagFileWatchers = make(map[string]*tagFileWatcher)
	e.driverReg.driverConfigs = make(map[string]core.DriverConfig)
	e.stats.readLatency = metrics.NewLatencyHistogram(metrics.DefaultReadLatencyBuckets)
	e.stats.publishLatency = metrics.NewLatencyHistogram(metrics.DefaultPublishLatencyBuckets)
	e.stats.dataAge = metrics.NewLatencyHistogram(metrics.DefaultDataAgeBuckets)
	return e
}
