// Package engine implements the CoreC orchestration engine.
//
// This file holds the command-path tests for stage-B audit findings:
// B6-1 — configuration-class write failures (tag not found, read-only
// area, type mismatch) are dead-lettered immediately instead of being
// blind-retried writeRetryCount+1 times, while every other failure
// keeps the retry loop; and B3.2 — stranded commands (held by a
// listener blocked on the semaphore, or buffered in a removed
// transport's channel) are dead-lettered on listener exit instead of
// vanishing silently.
package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/common/testutil"

	"github.com/CoreC-Dev/CoreC/core"
)

// erroringDriver is a core.Driver whose Write fails every command with a
// fixed error message and records the number of write attempts, so tests
// can assert exactly how often the device was contacted.
type erroringDriver struct {
	mockDriver
	mu       sync.Mutex
	err      string
	attempts int
}

func (d *erroringDriver) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	d.mu.Lock()
	d.attempts += len(commands)
	err := d.err
	d.mu.Unlock()
	results := make([]core.WriteResult, len(commands))
	for i := range results {
		results[i] = core.WriteResult{Success: false, Error: err}
	}
	return results, nil
}

func (d *erroringDriver) writeAttempts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.attempts
}

// runFailingWrite executes a write against a driver that fails every
// command with the given error message, waits for the command to land in
// the dead-letter queue, and returns the driver plus the dead-letter
// entry for inspection.
func runFailingWrite(t *testing.T, errMsg string, retryCount int) (*erroringDriver, core.DeadLetterEntry) {
	t.Helper()
	e := newTestEngine(t)
	e.writes.writeRetryCount = retryCount
	drv := &erroringDriver{}
	drv.name = "opc"
	drv.err = errMsg
	e.driverReg.drivers["opc"] = drv

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 1.0}
	e.writes.executeWriteWithRetry(cmd)

	if !testutil.WaitFor(2*time.Second, func() bool {
		return len(e.DeadLetterEntries()) == 1
	}) {
		t.Fatalf("dead letter queue: got %d entries, want 1", len(e.DeadLetterEntries()))
	}
	entries := e.DeadLetterEntries()
	return drv, entries[0]
}

// TestExecuteWritePermanentErrorSkipsRetry verifies that a configuration-
// class failure ("tag not found") contacts the device exactly once and is
// dead-lettered immediately: retrying a command that references no
// configured tag can never succeed (stage-B audit finding B6-1).
func TestExecuteWritePermanentErrorSkipsRetry(t *testing.T) {
	drv, entry := runFailingWrite(t, "tag not found: setpoint", 3)
	if got := drv.writeAttempts(); got != 1 {
		t.Errorf("write attempts: got %d, want 1 (permanent errors must not be retried)", got)
	}
	if !strings.Contains(entry.Error, "tag not found") {
		t.Errorf("dead letter error: got %q, want 'tag not found' substring", entry.Error)
	}
	if entry.Attempts != 1 {
		t.Errorf("dead letter attempts: got %d, want 1", entry.Attempts)
	}
}

// TestExecuteWriteTransientErrorStillRetried verifies the fail-safe
// direction: a connection-class error (or any error not matching the
// permanent signatures) keeps the full retry loop — writeRetryCount=3
// means 4 device round-trips before the dead letter queue.
func TestExecuteWriteTransientErrorStillRetried(t *testing.T) {
	drv, entry := runFailingWrite(t, "write tcp: connection reset by peer", 3)
	if got := drv.writeAttempts(); got != 4 {
		t.Errorf("write attempts: got %d, want 4 (transient errors must keep the retry loop)", got)
	}
	if entry.Attempts != 4 {
		t.Errorf("dead letter attempts: got %d, want 4", entry.Attempts)
	}
}

// TestIsPermanentWriteErrorClassifier pins the exact classification
// boundary: every permanent signature from the three drivers' Write paths
// must match, and the transient messages used across the engine tests
// must not.
func TestIsPermanentWriteErrorClassifier(t *testing.T) {
	permanent := []string{
		"tag not found: pump", // modbus/s7/opcua
		"tag pump is in a read-only area (discrete input/input register)",                                           // modbus
		"tag pump: type string is not supported on coil area (only bool is supported on coil/discrete-input areas)", // modbus
		"expected bool value for tag pump",                      // modbus
		"unsupported write type: string",                        // modbus
		"failed to create variant: cannot map int to a variant", // opcua
		"s7 encode pump: cannot encode float64 into 2 bytes",    // s7
	}
	for _, msg := range permanent {
		if !isPermanentWriteError(msg) {
			t.Errorf("isPermanentWriteError(%q) = false, want true", msg)
		}
	}
	transient := []string{
		"device refused write",
		"write tcp: connection reset by peer",
		"opcua batch write failed: StatusBadServerNotConnected",
		"s7 write failed",
		"",
	}
	for _, msg := range transient {
		if isPermanentWriteError(msg) {
			t.Errorf("isPermanentWriteError(%q) = true, want false", msg)
		}
	}
}

// TestCommandListenerDeadLettersStrandedOnExit is the stage-B audit
// finding B3.2 regression test: commands must not vanish when a command
// listener exits (transport removal / engine shutdown). The listener
// holding a command while blocked on the concurrency semaphore must
// dead-letter that command, and everything still buffered behind it.
func TestCommandListenerDeadLettersStrandedOnExit(t *testing.T) {
	e := New().(*CoreCEngine)
	e.runtime.ctx, e.runtime.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		e.runtime.cancel()
		e.runtime.wg.Wait()
	})

	// A single-slot semaphore, pre-occupied: the listener receives the
	// first command and then blocks acquiring the slot, holding it.
	e.writes.commandSem = make(chan struct{}, 1)
	e.writes.commandSem <- struct{}{}
	t.Cleanup(func() { <-e.writes.commandSem }) // release for cleanliness

	tr := &mockTransport{name: "stranded-tr"}
	tr.cmdCh = make(chan core.WriteCommand, 10)
	tr.status = core.TransportStatus{Name: "stranded-tr", State: core.StateConnected}

	// Listener bound to its own cancelable transport context.
	tCtx, tcancel := context.WithCancel(context.Background())
	e.writes.startCommandListener(tr, tCtx)

	// First command is received but can never acquire a slot; the second
	// and third stay buffered behind it.
	tr.cmdCh <- core.WriteCommand{Driver: "plc", Tag: "held", Value: 1.0}
	tr.cmdCh <- core.WriteCommand{Driver: "plc", Tag: "buffered-1", Value: 2.0}
	tr.cmdCh <- core.WriteCommand{Driver: "plc", Tag: "buffered-2", Value: 3.0}

	// Give the listener time to receive the held command and block on
	// the semaphore. Polling instead of a fixed sleep keeps the test
	// robust under CPU oversubscription (stage-A audit lesson).
	deadline := time.Now().Add(2 * time.Second)
	for len(tr.cmdCh) != 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if len(tr.cmdCh) != 2 {
		t.Fatalf("listener never picked up the held command; buffer=%d", len(tr.cmdCh))
	}

	// Transport removed / shutdown: the listener must exit and
	// dead-letter the held command plus both buffered ones.
	tcancel()
	e.runtime.wg.Wait()

	entries := e.DeadLetterEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 stranded commands dead-lettered, got %d", len(entries))
	}
	got := map[string]bool{}
	for _, entry := range entries {
		if entry.Error != "transport removed before dispatch" {
			t.Errorf("entry %s error = %q, want %q", entry.Command.Tag, entry.Error, "transport removed before dispatch")
		}
		if entry.Command.Source != "command" {
			t.Errorf("entry %s source = %q, want %q (trust-boundary stamp)", entry.Command.Tag, entry.Command.Source, "command")
		}
		if entry.Attempts != 0 {
			t.Errorf("entry %s attempts = %d, want 0 (never dispatched)", entry.Command.Tag, entry.Attempts)
		}
		got[entry.Command.Tag] = true
	}
	for _, want := range []string{"held", "buffered-1", "buffered-2"} {
		if !got[want] {
			t.Errorf("stranded command %q missing from dead letter queue", want)
		}
	}
}
