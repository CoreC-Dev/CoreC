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

// A write the device answered and refused must not consume that driver's window.
func TestFailedWriteDoesNotConsumeTheWindow(t *testing.T) {
	e := newTestEngine(t)
	e.writes.pacer.setInterval(200 * time.Millisecond)
	installDriver(e, "plc", &refusingDriver{})

	// First write is admitted by the gate, then refused by the device. A device
	// refusal arrives as Success=false on the result, not as an error: WriteTag
	// only sets err when the call itself could not be made.
	res, err := e.WriteTag(context.Background(), core.WriteCommand{
		Driver: "plc", Tag: "t", Value: 1.0, Type: core.TypeFloat32,
	})
	if err != nil {
		t.Fatalf("setup: want a per-result refusal, got error %v", err)
	}
	if res == nil || res.Success {
		t.Fatalf("setup: want Success=false from the refusing device, got %+v", res)
	}

	// The very next attempt must be admissible. Nothing reached the device, so
	// nothing has been spaced out.
	_, err = e.WriteTag(context.Background(), core.WriteCommand{
		Driver: "plc", Tag: "t", Value: 2.0, Type: core.TypeFloat32,
	})
	if errors.Is(err, core.ErrWritePaced) {
		t.Fatalf("a write the device refused consumed the window: %v "+
			"(docs promise 失败写入不占用下一次机会)", err)
	}
}

// A write that errored before reaching the device is the same event from the
// pacer's point of view, and must also leave the window open.
func TestWriteErrorDoesNotConsumeTheWindow(t *testing.T) {
	e := newTestEngine(t)
	e.writes.pacer.setInterval(200 * time.Millisecond)
	installDriver(e, "plc", &brokenDriver{})

	_, err := e.WriteTag(context.Background(), core.WriteCommand{
		Driver: "plc", Tag: "t", Value: 1.0, Type: core.TypeFloat32,
	})
	if !errors.Is(err, errTransportDown) {
		t.Fatalf("setup: want the transport error, got %v", err)
	}

	_, err = e.WriteTag(context.Background(), core.WriteCommand{
		Driver: "plc", Tag: "t", Value: 2.0, Type: core.TypeFloat32,
	})
	if errors.Is(err, core.ErrWritePaced) {
		t.Fatalf("a write that never reached the device consumed the window: %v", err)
	}
}

// --- fixtures -----------------------------------------------------------

var (
	errDeviceRefused = errors.New("device refused write")
	errTransportDown = errors.New("transport down")
)

type refusingDriver struct{ mockDriver }

// Write reports the device refused every command: the PLC answered, and said no.
func (d *refusingDriver) Write(ctx context.Context, cmds []core.WriteCommand) ([]core.WriteResult, error) {
	out := make([]core.WriteResult, len(cmds))
	for i := range out {
		out[i] = core.WriteResult{Success: false, Error: errDeviceRefused.Error()}
	}
	return out, nil
}

type brokenDriver struct{ mockDriver }

// Write never reaches the device.
func (d *brokenDriver) Write(ctx context.Context, cmds []core.WriteCommand) ([]core.WriteResult, error) {
	return nil, errTransportDown
}

func installDriver(e *CoreCEngine, name string, d core.Driver) {
	e.driverReg.drivers[name] = d
	e.driverReg.driverConfigs[name] = core.DriverConfig{Name: name, Type: "mock"}
}
