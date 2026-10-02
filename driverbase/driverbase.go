// Package driverbase provides a reusable lifecycle skeleton for southbound
// drivers (Modbus, OPC UA, S7). Concrete drivers embed BaseDriver and supply
// protocol-specific hooks (connect, closeConn, init) during Init; the base
// provides Start/Stop/Restart/Status/reconnectLoop/handleConnectionLost.
//
// This eliminates ~176 lines of cross-driver lifecycle duplication (DUP-001)
// and the 3-line ParseReconnectSettings block repeated in each driver (DUP-004).
package driverbase

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// BaseDriver holds the shared lifecycle state and hooks for a southbound driver.
// Concrete drivers embed this struct and set the hook fields during Init.
type BaseDriver struct {
	mu         sync.RWMutex
	name       string
	driverType string
	config     core.DriverConfig
	logger     core.Logger

	state      core.ConnState
	lastRead   time.Time
	lastError  string
	readCount  atomic.Uint64
	errorCount atomic.Uint64
	reconnectCount atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg    sync.WaitGroup

	reconnectBackoff     time.Duration
	maxReconnectBackoff  time.Duration
	maxReconnectFailures int

	// Hooks set by the concrete driver during Init.
	connectFunc   func() error
	initFunc      func(context.Context, core.DriverConfig) error
	closeConnFunc func() // close protocol conn (called with mu held)
	extraShutdown func() // optional pre-lock teardown (opcua subscription)
	onConnLost    func() // optional post-unlock cleanup (opcua stopSubscription)
	tagCountFunc  func() int
}

// SetConnectFunc sets the protocol-specific connect function.
func (d *BaseDriver) SetConnectFunc(f func() error) { d.connectFunc = f }

// SetInitFunc sets the init function used by Restart.
func (d *BaseDriver) SetInitFunc(f func(context.Context, core.DriverConfig) error) { d.initFunc = f }

// SetCloseConnFunc sets the connection close hook (called with mu held).
func (d *BaseDriver) SetCloseConnFunc(f func()) { d.closeConnFunc = f }

// SetExtraShutdown sets the optional pre-lock teardown hook.
func (d *BaseDriver) SetExtraShutdown(f func()) { d.extraShutdown = f }

// SetOnConnLost sets the optional post-unlock cleanup hook.
func (d *BaseDriver) SetOnConnLost(f func()) { d.onConnLost = f }

// SetTagCountFunc sets the tag count function for Status().
func (d *BaseDriver) SetTagCountFunc(f func() int) { d.tagCountFunc = f }

// SetMeta sets the driver name, type, and config. Called during Init.
// If no logger has been injected via SetLogger, defaults to NoopLogger.
func (d *BaseDriver) SetMeta(name, driverType string, config core.DriverConfig) {
	d.name = name
	d.driverType = driverType
	if d.logger == nil {
		d.logger = core.NoopLogger{}
	}
	d.config = config
}

// SetLogger injects a structured logger. Called by the engine before Init.
func (d *BaseDriver) SetLogger(l core.Logger) {
	d.mu.Lock()
	d.logger = l
	d.mu.Unlock()
}

// ParseReconnectSettings reads reconnect-interval, reconnect-max-interval,
// and max-reconnect-failures from the settings map with core defaults.
// Replaces the identical 3-line block inlined in each driver's Init (DUP-004).
func (d *BaseDriver) ParseReconnectSettings(settings map[string]any) {
	d.reconnectBackoff = util.GetDurationSetting(settings, "reconnect-interval", core.DefaultReconnectBackoff)
	d.maxReconnectBackoff = util.GetDurationSetting(settings, "reconnect-max-interval", core.DefaultMaxReconnectBackoff)
	d.maxReconnectFailures = util.GetIntSetting(settings, "max-reconnect-failures", core.DefaultMaxReconnectFailures)
}

// Start attempts the initial connection. On failure, transitions to
// StateConnecting and starts the reconnect loop in the background.
func (d *BaseDriver) Start(ctx context.Context) error {
	d.ctx, d.cancel = context.WithCancel(ctx)
	if err := d.connectFunc(); err != nil {
		d.logger.Warn("initial connect failed, will retry in background",
			"type", d.driverType, "name", d.name, "error", err)
		d.mu.Lock()
		d.state = core.StateConnecting
		d.lastError = err.Error()
		d.mu.Unlock()
		d.startReconnectLoop()
		return nil
	}
	d.logger.Info("driver started", "type", d.driverType, "name", d.name)
	return nil
}

// Stop cancels the context, waits for the reconnect goroutine, runs
// extraShutdown (if set), then closes the protocol connection.
func (d *BaseDriver) Stop() error {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	if d.extraShutdown != nil {
		d.extraShutdown()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closeConnFunc != nil {
		d.closeConnFunc()
	}
	d.state = core.StateDisconnected
	d.logger.Info("driver stopped", "type", d.driverType, "name", d.name)
	return nil
}

// Restart stops the driver, re-initializes with the new config, and starts again.
func (d *BaseDriver) Restart(ctx context.Context, config core.DriverConfig) error {
	if err := d.Stop(); err != nil {
		return err
	}
	if err := d.initFunc(ctx, config); err != nil {
		return err
	}
	return d.Start(ctx)
}

// Status returns the current driver status snapshot.
func (d *BaseDriver) Status() core.DriverStatus {
	d.mu.RLock()
	defer d.mu.RUnlock()
	tagCount := 0
	if d.tagCountFunc != nil {
		tagCount = d.tagCountFunc()
	}
	return core.DriverStatus{
		Name:           d.name,
		Type:           d.driverType,
		State:          d.state,
		LastRead:       d.lastRead,
		LastError:      d.lastError,
		TagCount:       tagCount,
		ReadCount:      d.readCount.Load(),
		ErrorCount:     d.errorCount.Load(),
		ReconnectCount: d.reconnectCount.Load(),
	}
}

// Name returns the driver instance name.
func (d *BaseDriver) Name() string { return d.name }

// Type returns the driver type string.
func (d *BaseDriver) Type() string { return d.driverType }

// reconnectLoop runs the shared reconnect loop with circuit breaker.
func (d *BaseDriver) reconnectLoop() {
	util.ReconnectLoopOpts(d.ctx, d.name, d.connectFunc,
		util.ReconnectOpts{
			InitialBackoff: d.reconnectBackoff,
			MaxBackoff:     d.maxReconnectBackoff,
			MaxFailures:    d.maxReconnectFailures,
		}, &d.reconnectCount)
}

// startReconnectLoop launches reconnectLoop in a tracked goroutine.
func (d *BaseDriver) startReconnectLoop() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.reconnectLoop()
	}()
}

// HandleConnectionLost transitions to StateError, closes the connection,
// runs onConnLost (if set), and starts the reconnect loop.
// Exported so concrete drivers can call it from Read/Write error paths.
func (d *BaseDriver) HandleConnectionLost() {
	d.mu.Lock()
	if d.state == core.StateConnecting || d.state == core.StateError {
		d.mu.Unlock()
		return
	}
	d.state = core.StateError
	if d.closeConnFunc != nil {
		d.closeConnFunc()
	}
	d.mu.Unlock()
	if d.onConnLost != nil {
		d.onConnLost()
	}
	d.logger.Warn("connection lost, starting reconnect", "type", d.driverType, "name", d.name)
	d.startReconnectLoop()
}

// --- Helper methods for concrete drivers ---

// SetState updates the connection state (thread-safe).
func (d *BaseDriver) SetState(s core.ConnState) {
	d.mu.Lock()
	d.state = s
	d.mu.Unlock()
}

// SetStateLocked sets state without locking (caller must hold mu).
func (d *BaseDriver) SetStateLocked(s core.ConnState) { d.state = s }

// GetState returns the current connection state (thread-safe).
func (d *BaseDriver) GetState() core.ConnState {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.state
}

// SetLastError records the last error message (thread-safe).
func (d *BaseDriver) SetLastError(err string) {
	d.mu.Lock()
	d.lastError = err
	d.mu.Unlock()
}

// SetLastErrorLocked sets lastError without locking (caller must hold mu).
func (d *BaseDriver) SetLastErrorLocked(err string) { d.lastError = err }

// RecordRead updates lastRead and increments readCount (caller holds mu or
// ensures no concurrent access to lastRead).
func (d *BaseDriver) RecordRead() {
	d.lastRead = time.Now()
	d.readCount.Add(1)
}

// SetLastRead sets the last read timestamp (caller holds mu).
func (d *BaseDriver) SetLastRead(t time.Time) { d.lastRead = t }

// AddReadCount adds n to the read counter.
func (d *BaseDriver) AddReadCount(n uint64) { d.readCount.Add(n) }

// RecordError increments errorCount.
func (d *BaseDriver) RecordError() {
	d.errorCount.Add(1)
}

// ReconnectCount returns the current reconnect count.
func (d *BaseDriver) ReconnectCount() uint64 { return d.reconnectCount.Load() }

// ReconnectBackoff returns the initial reconnect backoff interval.
func (d *BaseDriver) ReconnectBackoff() time.Duration { return d.reconnectBackoff }

// Context returns the driver's context (set during Start).
func (d *BaseDriver) Context() context.Context { return d.ctx }

// Config returns the driver's config.
func (d *BaseDriver) Config() core.DriverConfig { return d.config }

// Logger returns the driver's logger.
func (d *BaseDriver) Logger() core.Logger { return d.logger }

// Lock acquires the write lock.
func (d *BaseDriver) Lock()   { d.mu.Lock() }

// Unlock releases the write lock.
func (d *BaseDriver) Unlock() { d.mu.Unlock() }

// RLock acquires the read lock.
func (d *BaseDriver) RLock()   { d.mu.RLock() }

// RUnlock releases the read lock.
func (d *BaseDriver) RUnlock() { d.mu.RUnlock() }
