package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine/statistic"
)

// These tests exercise engine.rollbackStart, which cleans up partially
// started components when Start fails mid-way (M14). They construct a
// CoreCEngine directly (there is no public constructor for partial state)
// and call rollbackStart, then assert the engine is left in a clean,
// stopped state with no leaked maps or live context.

// rollbackErrDriver is a mockDriverWithState whose Stop returns an error,
// used to exercise rollbackStart's per-driver error-logging branch.
type rollbackErrDriver struct {
	mockDriverWithState
}

func (d *rollbackErrDriver) Stop() error { return errors.New("driver stop failed") }

// rollbackErrTransport is a mockTransportWithData whose Stop returns an
// error, used to exercise rollbackStart's per-transport error-logging
// branch.
type rollbackErrTransport struct {
	mockTransportWithData
}

func (t *rollbackErrTransport) Stop() error { return errors.New("transport stop failed") }

// Compile-time assertions that the erroring mocks still satisfy the
// interfaces whose maps they are placed into.
var (
	_ core.Driver    = (*rollbackErrDriver)(nil)
	_ core.Transport = (*rollbackErrTransport)(nil)
)

// newRollbackEngine builds a CoreCEngine with initialised maps and a live
// context/cancel, in the Running state, ready for rollbackStart tests to
// populate with components.
func newRollbackEngine() *CoreCEngine {
	e := &CoreCEngine{
		drivers:    make(map[string]core.Driver),
		transports: make(map[string]core.Transport),
		batchers:   make(map[string]*transportBatcher),
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.status = core.EngineStatusRunning
	return e
}

// TestRollbackStartWithComponents verifies that rollbackStart stops a
// started scheduler, started driver, started transport, and a batcher,
// then clears every component map, closes the data bus, cancels the
// context, and sets the status to stopped.
func TestRollbackStartWithComponents(t *testing.T) {
	e := newRollbackEngine()

	// Started scheduler (no tasks → no goroutines, Stop is a clean no-op).
	e.scheduler = NewScheduler(nil, nil, 0, statistic.NewManager())
	if err := e.scheduler.Start(e.ctx); err != nil {
		t.Fatalf("scheduler start failed: %v", err)
	}

	// Data bus.
	e.dataBus = NewDataBus(64)

	// A started mock driver and transport in the component maps.
	d := &mockDriverWithState{}
	d.name = "d1"
	e.drivers["d1"] = d

	tr := &mockTransportWithData{}
	tr.name = "t1"
	e.transports["t1"] = tr

	// A batcher. It is intentionally not started (no flushLoop goroutine);
	// stop() is a safe no-op on an unstarted batcher with an empty buffer,
	// so this exercises the batcher-stop loop without leaking goroutines.
	e.batchers["t1"] = &transportBatcher{transport: tr, batchSize: 10}

	e.rollbackStart()

	if len(e.drivers) != 0 {
		t.Errorf("drivers map not cleared: got %d entries", len(e.drivers))
	}
	if len(e.transports) != 0 {
		t.Errorf("transports map not cleared: got %d entries", len(e.transports))
	}
	if len(e.batchers) != 0 {
		t.Errorf("batchers map not cleared: got %d entries", len(e.batchers))
	}
	if e.status != core.EngineStatusStopped {
		t.Errorf("status = %v, want %v", e.status, core.EngineStatusStopped)
	}
	if e.ctx.Err() == nil {
		t.Error("engine context was not cancelled by rollback")
	}
}

// TestRollbackStartNilSchedulerAndDataBus verifies that rollbackStart
// handles a nil scheduler and nil data bus without panicking (the
// partially-started state where Start failed before the scheduler or
// data bus were created).
func TestRollbackStartNilSchedulerAndDataBus(t *testing.T) {
	e := newRollbackEngine()
	// e.scheduler and e.dataBus remain nil.

	e.rollbackStart()

	if e.status != core.EngineStatusStopped {
		t.Errorf("status = %v, want %v", e.status, core.EngineStatusStopped)
	}
	if e.ctx.Err() == nil {
		t.Error("engine context was not cancelled by rollback")
	}
	if len(e.drivers) != 0 || len(e.transports) != 0 || len(e.batchers) != 0 {
		t.Errorf("component maps not empty after rollback: drivers=%d transports=%d batchers=%d",
			len(e.drivers), len(e.transports), len(e.batchers))
	}
}

// TestRollbackStartEmpty verifies rollbackStart on a bare engine with no
// components at all (no cancel, no scheduler, no data bus, empty maps)
// does not panic and leaves the status stopped.
func TestRollbackStartEmpty(t *testing.T) {
	e := &CoreCEngine{
		drivers:    make(map[string]core.Driver),
		transports: make(map[string]core.Transport),
		batchers:   make(map[string]*transportBatcher),
	}
	// e.cancel, e.scheduler, e.dataBus all nil.

	e.rollbackStart()

	if e.status != core.EngineStatusStopped {
		t.Errorf("status = %v, want %v", e.status, core.EngineStatusStopped)
	}
}

// TestRollbackStartWithErroringComponents verifies that rollbackStart
// logs but tolerates Stop errors from drivers and transports, still
// clearing the maps and setting the status to stopped.
func TestRollbackStartWithErroringComponents(t *testing.T) {
	e := newRollbackEngine()

	e.drivers["bad-d"] = &rollbackErrDriver{}
	e.transports["bad-t"] = &rollbackErrTransport{}

	e.rollbackStart()

	if len(e.drivers) != 0 {
		t.Errorf("drivers map not cleared: got %d entries", len(e.drivers))
	}
	if len(e.transports) != 0 {
		t.Errorf("transports map not cleared: got %d entries", len(e.transports))
	}
	if e.status != core.EngineStatusStopped {
		t.Errorf("status = %v, want %v", e.status, core.EngineStatusStopped)
	}
	if e.ctx.Err() == nil {
		t.Error("engine context was not cancelled by rollback")
	}
}

// TestRollbackStartIntegration drives rollbackStart through the real
// Start() path: a config with an unknown transport type fails in
// startTransports (after the scheduler has already started), so Start's
// deferred rollback must clean up the scheduler, data bus, and context,
// leaving the engine fully stopped with no leftover transports.
func TestRollbackStartIntegration(t *testing.T) {
	eng := New()
	e := eng.(*CoreCEngine)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &core.Config{
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "nonexistent-transport-xyz"},
		},
	}

	err := eng.Start(ctx, cfg)
	if err == nil {
		_ = eng.Stop()
		t.Fatal("expected Start to fail for an unknown transport type, but it succeeded")
	}

	// rollbackStart (run via Start's defer) must have torn everything down.
	if eng.Status() != core.EngineStatusStopped {
		t.Errorf("status = %v, want %v", eng.Status(), core.EngineStatusStopped)
	}
	if n := len(eng.ListTransports()); n != 0 {
		t.Errorf("transports not cleared after rollback: got %d", n)
	}
	if n := len(eng.ListDrivers()); n != 0 {
		t.Errorf("drivers not cleared after rollback: got %d", n)
	}
	// The engine context must be cancelled so no partially-started
	// goroutine keeps running.
	if e.ctx.Err() == nil {
		t.Error("engine context was not cancelled after failed Start")
	}
}
