package engine

// docs/config/global.md, the write-min-interval section, promises in one clause
// what pacing is FOR:
//
//   时间窗按驱动名（即设备）独立计算——只有被接受的写入才推进该设备的时间窗，
//   失败写入不占用下一次机会。
//   ("the window is per driver; only an ACCEPTED write advances that device's
//    clock, a failed write does not consume the next opportunity")
//
// The reason matters more than the wording. The floor exists for an old PLC that
// serves one command per CPU loop. If a write the PLC REFUSED also burns the slot,
// the caller waits a full interval to discover the device was never going to take
// it, and an operator watching two failed writes 200ms apart against a 200ms floor
// cannot tell pacing from a device that is refusing to talk. Those two diagnoses
// call for opposite actions.
//
// "Accepted" therefore has to mean accepted BY THE DEVICE, not "waved through the
// gate". Passing the gate is our bookkeeping; the device taking it is the event the
// floor exists to space out. A failed write has spent nothing, so it must not close
// the window.
//
// write_pacing.go's own header already claims this ("Only accepted writes advance
// the clock, so a failed write does not consume the next writer's turn"). These
// tests hold the code to the sentence that both the docs and the comment make.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

var (
	errDeviceRefused = errors.New("device refused write")
	errTransportDown = errors.New("transport down")
)

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

// And the inverse, which must stay true: once the device ACCEPTS a write the next
// one is paced. Without this the fix above could amount to "never pace anything".
func TestAcceptedWriteClosesTheWindow(t *testing.T) {
	e := newTestEngine(t)
	e.writes.pacer.setInterval(200 * time.Millisecond)
	registerTaggedDriver(e, "plc", nil)

	if _, err := e.WriteTag(context.Background(), core.WriteCommand{
		Driver: "plc", Tag: "t", Value: 1.0, Type: core.TypeFloat32,
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	_, err := e.WriteTag(context.Background(), core.WriteCommand{
		Driver: "plc", Tag: "t", Value: 2.0, Type: core.TypeFloat32,
	})
	if !errors.Is(err, core.ErrWritePaced) {
		t.Fatalf("write immediately after an accepted one: err = %v, want ErrWritePaced", err)
	}
}

// --- fixtures -----------------------------------------------------------

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
