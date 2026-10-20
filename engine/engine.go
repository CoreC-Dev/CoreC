package engine

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/CoreC-Dev/CoreC/common/metrics"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine/statistic"
	"github.com/CoreC-Dev/CoreC/rule"
)

var _ core.Engine = (*CoreCEngine)(nil)

// CoreCEngine implements core.Engine.
// This is the central orchestrator.
type CoreCEngine struct {
	// mu guards the composite state shared across roles: the driver and
	// transport registries, the status, the derived evaluator, and the
	// alert handler list. It lives on the composition root so every
	// role can reach it through its back-pointer.
	mu sync.RWMutex

	// Roles (CPLX-020 God Object split). Every method on CoreCEngine is
	// a <=3-line forwarder to one of these; all logic lives in the
	// role structs (see docs/exec-plans/tech-debt-tracker.md CPLX-020).
	runtime      *engineRuntime
	ruleSet      *ruleRole
	reads        *readAccessor
	stats        *statsCollector
	driverReg    *driverRegistry
	transportMgr *transportManager
	writes       *writeManager
	processing   *processingCore
	lifecycle    *lifecycleSupport
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
		runtime:      &engineRuntime{},
		ruleSet:      &ruleRole{},
		reads:        &readAccessor{},
		stats:        &statsCollector{},
		driverReg:    &driverRegistry{},
		transportMgr: &transportManager{},
		writes:       &writeManager{},
		processing:   &processingCore{},
		lifecycle:    &lifecycleSupport{},
	}
	e.reads.eng = e
	e.stats.eng = e
	e.driverReg.eng = e
	e.transportMgr.eng = e
	e.writes.eng = e
	e.processing.eng = e
	e.lifecycle.eng = e
	e.runtime.eng = e
	return e
}

// New creates a new CoreC Engine instance.
func New() core.Engine {
	e := newEngineShell()
	e.runtime.status = core.EngineStatusStopped
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
	e.runtime.shutdownTimeout = core.DefaultShutdownTimeout
	e.lifecycle.errorThrottleWin = core.DefaultErrorThrottleWindow
	e.lifecycle.defaultTagInterval = core.DefaultTagInterval
	e.processing.badQualityPolicy = badQualityPublish
	e.writes.writeRetryCount = 3
	e.writes.commandConcurrency = core.DefaultCommandConcurrency
	e.processing.numHighPriorityWorkers = core.DefaultHighPriorityWorkers
	e.stats.statManager = statistic.NewManager()
	e.writes.deadLetterMaxLen = core.DefaultDeadLetterMaxLen
	e.driverReg.tagFileWatchers = make(map[string]*tagFileWatcher)
	e.driverReg.driverConfigs = make(map[string]core.DriverConfig)
	e.stats.readRate = metrics.NewRateWindow()
	e.stats.labeled = newLabeledLatencyStore()
	e.processing.alerts = newAlertManager(0)
	e.processing.alertActiveWindow = core.DefaultAlertActiveWindow
	return e
}

// --- CoreCEngine forwarders (core.Lifecycler) ---

func (e *CoreCEngine) Start(ctx context.Context, config *core.Config) error {
	return e.runtime.Start(ctx, config)
}

func (e *CoreCEngine) Stop() error {
	return e.runtime.Stop()
}

func (e *CoreCEngine) Reload(config *core.Config) error {
	return e.runtime.Reload(config)
}

func (e *CoreCEngine) Suspend() error {
	return e.runtime.Suspend()
}

func (e *CoreCEngine) Resume() error {
	return e.runtime.Resume()
}
