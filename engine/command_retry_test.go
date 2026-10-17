// Package engine implements the CoreC orchestration engine.
//
// This file holds the retry-classification tests for executeWriteWithRetry
// (stage-B audit finding B6-1): configuration-class write failures (tag not
// found, read-only area, type mismatch) are dead-lettered immediately
// instead of being blind-retried writeRetryCount+1 times, while every
// other failure keeps the retry loop.
package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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
	e.writeRetryCount = retryCount
	drv := &erroringDriver{}
	drv.name = "opc"
	drv.err = errMsg
	e.drivers["opc"] = drv

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 1.0}
	e.executeWriteWithRetry(cmd)

	if !waitFor(2*time.Second, func() bool {
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
