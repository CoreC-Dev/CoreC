package engine

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine/statistic"
)

var _ core.Scheduler = (*scheduler)(nil)

// scheduler implements core.Scheduler.
// It manages periodic data collection tasks using goroutines and tickers.
type scheduler struct {
	mu           sync.RWMutex
	tasks        map[string]*taskRunner
	paused       map[string]bool
	globalPaused atomic.Bool
	readFunc     func(ctx context.Context, driver string, tags []string) ([]core.TagValue, error)
	onData       func(driver string, values []core.TagValue)

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

	// Overrun throttling state
	lastOverrunLog time.Time
	overrunCount   uint64

	inError           bool
	consecutiveErrors int  // consecutive error ticks for auto-degrade
	degraded          bool // currently running at degraded interval
}

// NewScheduler creates a new scheduler.
// readFunc is called to read tags from a driver.
// onData is called when data is received.
// errorThrottleWindow is the time window for suppressing repeated error
// logs; <=0 falls back to core.DefaultErrorThrottleWindow.
func NewScheduler(
	readFunc func(ctx context.Context, driver string, tags []string) ([]core.TagValue, error),
	onData func(driver string, values []core.TagValue),
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

func (s *scheduler) runTask(ctx context.Context, runner *taskRunner) { //nolint:gocyclo // task lifecycle (connect/read/transform/publish/retry); complexity 27. Refactor tracked as tech debt.
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
			// configured (>0), otherwise fall back to the interval. This
			// decouples the read deadline from the collection cadence.
			readTimeout := task.ReadTimeout
			if readTimeout <= 0 {
				readTimeout = interval
			}
			readCtx, readCancel := context.WithTimeout(ctx, readTimeout)
			values, err := s.readFunc(readCtx, task.Driver, task.Tags)
			readCancel()

			latency := time.Since(start)

			if latency > interval {
				runner.overrunCount++
				if time.Since(runner.lastOverrunLog) >= s.errorThrottleWindow {
					slog.Warn("scheduler overrun",
						"task", task.ID,
						"latency", latency,
						"interval", interval,
						"occurrences", runner.overrunCount,
					)
					runner.lastOverrunLog = time.Now()
					runner.overrunCount = 0
				}
			}

			if err != nil {
				statistic.DefaultManager.PushError()
				errMsg := err.Error()
				runner.errCount++
				now := time.Now()

				// Log immediately on first failure or when error message changes,
				// otherwise throttle to once per errorThrottleWindow.
				if !runner.inError || errMsg != runner.lastErrMsg || now.Sub(runner.lastErrLog) >= s.errorThrottleWindow {
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
				continue
			}

			// If recovered from error
			if runner.inError {
				slog.Info("read recovered",
					"task", task.ID,
					"driver", task.Driver,
				)
				runner.inError = false
				runner.lastErrMsg = ""
				runner.errCount = 0
				runner.consecutiveErrors = 0

				// Restore original interval if we were degraded.
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

			if len(values) > 0 {
				statistic.DefaultManager.PushRead(int64(len(values)))

				// Apply deadband filtering: skip values that haven't changed
				// beyond the configured threshold since last report.
				// A fresh slice is allocated so the filter never aliases
				// the driver's returned slice (a driver may legitimately
				// reuse its return buffer across Read calls).
				if len(runner.task.DeadBands) > 0 {
					filtered := make([]core.TagValue, 0, len(values))
					for _, v := range values {
						threshold, ok := runner.task.DeadBands[v.Tag]
						if !ok || threshold <= 0 {
							filtered = append(filtered, v)
							continue
						}
						numVal := util.ToFloat64(v.Value)
						lastVal, hasLast := runner.lastValues[v.Tag]
						if !hasLast || math.Abs(numVal-lastVal) >= threshold {
							filtered = append(filtered, v)
							runner.lastValues[v.Tag] = numVal
						}
					}
					values = filtered
				}

				if len(values) > 0 {
					s.onData(task.Driver, values)
				}
			}
		}
	}
}
