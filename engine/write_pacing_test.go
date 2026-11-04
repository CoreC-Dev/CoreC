package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestWritePacingRefusesBackToBackWrites: with write-min-interval set, a
// second write to the same device inside the window is refused with the
// ErrWritePaced sentinel and never reaches the driver.
func TestWritePacingRefusesBackToBackWrites(t *testing.T) {
	e := newTestEngine(t)
	e.writes.pacer.setInterval(200 * time.Millisecond)
	d := registerTaggedDriver(e, "plc", nil)

	if _, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "plc", Tag: "t1", Value: 1.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("first write must pass: %v", err)
	}
	_, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "plc", Tag: "t1", Value: 2.0, Type: core.TypeFloat32})
	if !errors.Is(err, core.ErrWritePaced) {
		t.Fatalf("second immediate write: err = %v, want ErrWritePaced", err)
	}
	if got := len(d.GetWritten()); got != 1 {
		t.Errorf("paced write must not reach the driver: got %d writes", got)
	}
}

// TestWritePacingAllowsAfterWindow: once the floor elapses the next write is
// accepted again, and pacing is per device (a second driver is unaffected).
func TestWritePacingAllowsAfterWindow(t *testing.T) {
	e := newTestEngine(t)
	e.writes.pacer.setInterval(40 * time.Millisecond)
	d1 := registerTaggedDriver(e, "plc1", nil)
	d2 := registerTaggedDriver(e, "plc2", nil)

	ctx := context.Background()
	if _, err := e.WriteTag(ctx, core.WriteCommand{Driver: "plc1", Tag: "t", Value: 1.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// A different device is not paced by plc1's turn.
	if _, err := e.WriteTag(ctx, core.WriteCommand{Driver: "plc2", Tag: "t", Value: 1.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("other driver must not be paced: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := e.WriteTag(ctx, core.WriteCommand{Driver: "plc1", Tag: "t", Value: 2.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("write after the window must pass: %v", err)
	}
	if len(d1.GetWritten()) != 2 || len(d2.GetWritten()) != 1 {
		t.Errorf("writes reaching drivers = %d/%d, want 2/1", len(d1.GetWritten()), len(d2.GetWritten()))
	}
}

// TestWritePacingDisabledByDefault pins backward compatibility: unset
// write-min-interval means back-to-back writes proceed as before.
func TestWritePacingDisabledByDefault(t *testing.T) {
	e := newTestEngine(t)
	d := registerTaggedDriver(e, "plc", nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := e.WriteTag(ctx, core.WriteCommand{Driver: "plc", Tag: "t", Value: float64(i), Type: core.TypeFloat32}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := len(d.GetWritten()); got != 3 {
		t.Errorf("writes reaching the driver = %d, want 3", got)
	}
	if e.writes.pacer.currentInterval() != 0 {
		t.Error("pacing must be disabled by default")
	}
}

// TestWritePacingAppliedFromConfig checks the YAML key reaches the pacer
// through applyConfig and that a malformed value fails startup.
func TestWritePacingAppliedFromConfig(t *testing.T) {
	e := newTestEngine(t)
	if err := e.writes.applyConfig(&core.EngineConfig{WriteMinInterval: "150ms"}); err != nil {
		t.Fatalf("valid write-min-interval rejected: %v", err)
	}
	if got := e.writes.pacer.currentInterval(); got != 150*time.Millisecond {
		t.Errorf("pacer interval = %v, want 150ms", got)
	}

	e2 := newTestEngine(t)
	err := e2.writes.applyConfig(&core.EngineConfig{WriteMinInterval: "not-a-duration"})
	if err == nil {
		t.Fatal("malformed write-min-interval must fail startup")
	}
}
