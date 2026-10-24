package engine

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// scheduler manages timed data collection tasks.
// It manages periodic data collection tasks using goroutines and tickers.
type scheduler struct {
	mu           sync.RWMutex
	tasks        map[string]*taskRunner
	paused       map[string]bool
	globalPaused atomic.Bool
	readFunc     func(ctx context.Context, driver string, tags []string) ([]core.TagValue, error)
	onData       func(driver string, values []core.TagValue, priority int)

	// errorThrottleWindow is the time window for suppressing repeated
	// error/overrun log messages. Defaults to core.DefaultErrorThrottleWindow.
	errorThrottleWindow time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	// wg tracks all runTask goroutines so Stop() can wait for
	// them to fully exit before returning.  Without this, a
	// runTask goroutine may still call onData (→ DataBus.Push)
	// after the engine has called DataBus.Close(), causing a
	// send-on-closed-channel panic / data race.
	wg sync.WaitGroup
}

type taskRunner struct {
	task   core.ScheduleTask
	cancel context.CancelFunc

	// Deadband: last reported numeric value per tag
	lastValues map[string]float64

	// Error throttling state
	lastErrLog time.Time
	lastErrMsg string
	errCount   uint64

	// Overrun throttling state. overrunCount is the windowed counter
	// printed by the throttled log (reset each time the log fires);
	// overrunsTotal is the cumulative lifetime count exposed via
	// Health() for Prometheus — never reset (a windowed counter alone
	// would silently lose history after each log).
	lastOverrunLog time.Time
	overrunCount   uint64
	overrunsTotal  uint64

	inError           bool
	consecutiveErrors int  // consecutive error ticks for auto-degrade
	degraded          bool // currently running at degraded interval
}

// NewScheduler creates a new scheduler.
// readFunc is called to read tags from a driver.
// onData is called when data is received, with the task's priority so the
// engine can route fast-interval data to a dedicated high-priority channel.
// errorThrottleWindow is the time window for suppressing repeated error
// logs; <=0 falls back to core.DefaultErrorThrottleWindow.
func NewScheduler(
	readFunc func(ctx context.Context, driver string, tags []string) ([]core.TagValue, error),
	onData func(driver string, values []core.TagValue, priority int),
	errorThrottleWindow time.Duration,
) *scheduler {
	if errorThrottleWindow <= 0 {
		errorThrottleWindow = core.DefaultErrorThrottleWindow
	}
	return &scheduler{
		tasks:               make(map[string]*taskRunner),
		paused:              make(map[string]bool),
		readFunc:            readFunc,
		onData:              onData,
		errorThrottleWindow: errorThrottleWindow,
	}
}

func (s *scheduler) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)
	slog.Info("scheduler started")
	return nil
}

func (s *scheduler) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	for _, t := range s.tasks {
		t.cancel()
	}
	s.tasks = make(map[string]*taskRunner)
	s.mu.Unlock()

	// Wait for all runTask goroutines to exit so that no goroutine
	// is still calling onData (→ DataBus.Push) when the caller
	// subsequently closes the DataBus.
	s.wg.Wait()

	slog.Info("scheduler stopped")
	return nil
}

func (s *scheduler) AddTask(task core.ScheduleTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If task already exists, remove it first
	if existing, ok := s.tasks[task.ID]; ok {
		existing.cancel()
	}

	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	taskCtx, taskCancel := context.WithCancel(ctx)
	runner := &taskRunner{
		task:       task,
		cancel:     taskCancel,
		lastValues: make(map[string]float64),
	}
	s.tasks[task.ID] = runner

	s.wg.Add(1)
	go s.runTask(taskCtx, runner)

	slog.Info("task added", "id", task.ID, "driver", task.Driver, "interval", task.Interval, "tags", len(task.Tags))
	return nil
}

// Health aggregates per-driver scheduler health for Prometheus
// exposure (core.SchedulerHealthProvider). Safe on a nil scheduler (an
// engine that has not started has no scheduler yet): returns nil so
// the metrics route simply omits the families.
//
// Aggregation per driver across all of its tasks: Degraded is true if
// ANY task is auto-degraded; ConsecutiveErrors is the worst streak
// (max) so a single sick task cannot hide behind healthy siblings;
// OverrunsTotal is the sum of the drivers' lifetime overrun counts.
// Drivers are emitted in sorted name order for deterministic output.
func (s *scheduler) Health() []core.TaskHealth {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.tasks) == 0 {
		return nil
	}
	byDriver := make(map[string]*core.TaskHealth, len(s.tasks))
	for _, t := range s.tasks {
		h, ok := byDriver[t.task.Driver]
		if !ok {
			h = &core.TaskHealth{Driver: t.task.Driver}
			byDriver[t.task.Driver] = h
		}
		if t.degraded {
			h.Degraded = true
		}
		if t.consecutiveErrors > h.ConsecutiveErrors {
			h.ConsecutiveErrors = t.consecutiveErrors
		}
		h.OverrunsTotal += t.overrunsTotal
	}

	names := make([]string, 0, len(byDriver))
	for name := range byDriver {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]core.TaskHealth, 0, len(names))
	for _, name := range names {
		out = append(out, *byDriver[name])
	}
	return out
}

func (s *scheduler) Pause() error {
	s.globalPaused.Store(true)
	slog.Info("scheduler paused globally")
	return nil
}

func (s *scheduler) Resume() error {
	s.globalPaused.Store(false)
	slog.Info("scheduler resumed globally")
	return nil
}

// RemoveDriverTasks cancels and removes all scheduler tasks belonging to
// the named driver. Unlike PauseDriver, which only sets a flag and leaves
// the task goroutines running, this actually cancels their contexts and
// removes them from the tasks map so the goroutines exit. This should be
// called by RemoveDriver to prevent goroutine leaks when a driver is
// permanently removed (problem 3).
func (s *scheduler) RemoveDriverTasks(driverName string) {
	s.mu.Lock()
	for id, t := range s.tasks {
		if t.task.Driver == driverName {
			t.cancel()
			delete(s.tasks, id)
		}
	}
	delete(s.paused, driverName)
	s.mu.Unlock()
}

func (s *scheduler) PauseDriver(name string) error {
	s.mu.Lock()
	s.paused[name] = true
	s.mu.Unlock()
	slog.Info("driver paused", "driver", name)
	return nil
}

// ResumeDriver clears the driver's paused flag so its tasks resume at
// their next tick. Pause/resume must be API-reachable, not
// engine-internal only — maintenance scenarios otherwise have to
// rewrite the whole config to stop collecting from a device under
// repair.
func (s *scheduler) ResumeDriver(name string) error {
	s.mu.Lock()
	delete(s.paused, name)
	s.mu.Unlock()
	slog.Info("driver resumed", "driver", name)
	return nil
}

// PausedDrivers returns the sorted names of currently-paused drivers.
func (s *scheduler) PausedDrivers() []string {
	s.mu.RLock()
	names := make([]string, 0, len(s.paused))
	for name, paused := range s.paused {
		if paused {
			names = append(names, name)
		}
	}
	s.mu.RUnlock()
	sort.Strings(names)
	return names
}

func (s *scheduler) runTask(ctx context.Context, runner *taskRunner) {
	defer s.wg.Done()

	task := runner.task
	interval := task.Interval
	if interval <= 0 {
		interval = core.DefaultTagInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Auto-degrade: after this many consecutive error ticks, stretch the
	// interval to reduce load on a failing driver. When the driver
	// recovers, the ticker is reset to the original interval.
	const degradeAfterErrors = 5
	degradedInterval := interval * 10
	if degradedInterval < 10*time.Second {
		degradedInterval = 10 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Check if scheduler is globally paused (e.g. during config reload)
		if s.globalPaused.Load() {
			continue
		}

		// Check if paused
		s.mu.RLock()
		paused := s.paused[task.Driver]
		s.mu.RUnlock()
		if paused {
			continue
		}

		start := time.Now()

		// Execute read with timeout. Use the task's ReadTimeout if
		// configured (>0), otherwise fall back to the interval.
		readTimeout := task.ReadTimeout
		if readTimeout <= 0 {
			readTimeout = interval
		}
		readCtx, readCancel := context.WithTimeout(ctx, readTimeout)
		values, err := s.readFunc(readCtx, task.Driver, task.Tags)
		readCancel()

		latency := time.Since(start)
		s.logOverrunIfNeeded(runner, task, latency, interval)

		if err != nil {
			handleReadError(runner, task, err, interval, degradedInterval, degradeAfterErrors, s.errorThrottleWindow, ticker)
			continue
		}

		// If recovered from error
		if runner.inError {
			handleRecovery(runner, task, interval, ticker)
		}

		if len(values) == 0 {
			continue
		}

		values = applyDeadbandFilter(runner, values)
		if len(values) > 0 {
			s.onData(task.Driver, values, task.Priority)
		}
	}
}

// logOverrunIfNeeded warns when a read takes longer than the collection interval.
func (s *scheduler) logOverrunIfNeeded(runner *taskRunner, task core.ScheduleTask, latency, interval time.Duration) {
	if latency <= interval {
		return
	}
	runner.overrunCount++
	runner.overrunsTotal++
	if time.Since(runner.lastOverrunLog) < s.errorThrottleWindow {
		return
	}
	slog.Warn("scheduler overrun",
		"task", task.ID,
		"latency", latency,
		"interval", interval,
		"occurrences", runner.overrunCount,
	)
	runner.lastOverrunLog = time.Now()
	runner.overrunCount = 0
}

// handleReadError records the error, logs it (throttled), and auto-degrades
// the ticker if sustained errors reach the threshold.
func handleReadError(runner *taskRunner, task core.ScheduleTask, err error, interval, degradedInterval time.Duration, degradeAfterErrors int, errorThrottleWindow time.Duration, ticker *time.Ticker) {
	errMsg := err.Error()
	runner.errCount++
	now := time.Now()

	// Log immediately on first failure or when error message changes,
	// otherwise throttle to once per errorThrottleWindow.
	if !runner.inError || errMsg != runner.lastErrMsg || now.Sub(runner.lastErrLog) >= errorThrottleWindow {
		slog.Error("read failed",
			"task", task.ID,
			"driver", task.Driver,
			"error", err,
			"repeated_count", runner.errCount,
		)
		runner.lastErrLog = now
		runner.lastErrMsg = errMsg
		runner.errCount = 0
	}
	runner.inError = true
	runner.consecutiveErrors++

	// Auto-degrade: if we've had enough consecutive errors and
	// haven't already degraded, stretch the tick interval.
	if runner.consecutiveErrors >= degradeAfterErrors && !runner.degraded {
		runner.degraded = true
		ticker.Reset(degradedInterval)
		slog.Warn("scheduler auto-degrade: stretching interval due to sustained errors",
			"task", task.ID,
			"driver", task.Driver,
			"original_interval", interval,
			"degraded_interval", degradedInterval,
			"consecutive_errors", runner.consecutiveErrors,
		)
	}
}

// handleRecovery logs the recovery and restores the original interval if degraded.
func handleRecovery(runner *taskRunner, task core.ScheduleTask, interval time.Duration, ticker *time.Ticker) {
	slog.Info("read recovered",
		"task", task.ID,
		"driver", task.Driver,
	)
	runner.inError = false
	runner.lastErrMsg = ""
	runner.errCount = 0
	runner.consecutiveErrors = 0

	if runner.degraded {
		runner.degraded = false
		ticker.Reset(interval)
		slog.Info("scheduler auto-degrade: restoring original interval",
			"task", task.ID,
			"driver", task.Driver,
			"interval", interval,
		)
	}
}

// applyDeadbandFilter skips values that haven't changed beyond the configured
// threshold since last report. A fresh slice is allocated so the filter never
// aliases the driver's returned slice.
//
// Bad or uncertain quality always passes through and never updates the
// baseline: swallowing the point would hide
// the quality degradation from the engine's bad-quality policies, and
// recording its value (bad values decode as 0 via util.ToFloat64) would
// poison the baseline so that subsequent GOOD values within the old→0
// delta could be swallowed as well — silently freezing the data stream.
// This matches the industrial convention that quality degradation
// penetrates the deadband: the operator must see the tag went bad.
//
// Non-numeric values (bool, string, bytes) also bypass the deadband and
// never update the baseline: the documented
// behaviour says deadband only applies to numeric types, and coercing
// non-numeric values through ToFloat64 (bool→0/1, string→0) would compare
// fabricated zeros — freezing the tag at its first sample forever.
func applyDeadbandFilter(runner *taskRunner, values []core.TagValue) []core.TagValue {
	if len(runner.task.DeadBands) == 0 {
		return values
	}
	filtered := make([]core.TagValue, 0, len(values))
	for _, v := range values {
		threshold, ok := runner.task.DeadBands[v.Tag]
		if !ok || threshold <= 0 {
			filtered = append(filtered, v)
			continue
		}
		if v.Quality != core.QualityGood {
			// Quality degradation always passes through; the baseline
			// is left untouched.
			filtered = append(filtered, v)
			continue
		}
		numVal, numeric := util.ToFloat64OK(v.Value)
		if !numeric {
			// Deadband is a numeric-only filter: non-numeric values
			// pass through on every sample without touching the
			// baseline.
			filtered = append(filtered, v)
			continue
		}
		lastVal, hasLast := runner.lastValues[v.Tag]
		if !hasLast || math.Abs(numVal-lastVal) >= threshold {
			filtered = append(filtered, v)
			runner.lastValues[v.Tag] = numVal
		}
	}
	return filtered
}
