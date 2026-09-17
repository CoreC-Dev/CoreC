// Package engine implements the CoreC orchestration engine.
package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// These tests exercise the chained-core command-passthrough path in
// executeWriteWithRetry: when a relay node has no local driver for a
// command target, the command is forwarded to downstream nodes via
// transports implementing core.CommandForwarder; if no forwarder is
// available (or all forwarders fail), the command lands in the dead
// letter queue. They use in-package mocks and require no external broker,
// so they must never be skipped.

// ─── Mocks ──────────────────────────────────────────────────────────

// failingDriver is a core.Driver whose Write always fails with a fixed
// error, used to exercise the retry + dead-letter path when a local driver
// exists but cannot fulfil the command. It records every attempt so tests
// can assert the retry count. It embeds mockDriver for the rest of the
// Driver surface.
type failingDriver struct {
	mockDriver
	mu         sync.Mutex
	writeCount int
}

func (d *failingDriver) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	d.mu.Lock()
	d.writeCount += len(commands)
	d.mu.Unlock()
	results := make([]core.WriteResult, len(commands))
	for i := range results {
		results[i] = core.WriteResult{Success: false, Error: "device refused write"}
	}
	return results, nil
}

func (d *failingDriver) writes() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writeCount
}

// forwarderTransport is a core.Transport that also implements
// core.CommandForwarder. It records every ForwardCommand call and can be
// configured to return an error to simulate a downstream publish failure.
type forwarderTransport struct {
	name       string
	mu         sync.Mutex
	forwarded  []core.WriteCommand
	forwardErr error
	status     core.TransportStatus
}

func newForwarderTransport(name string, err error) *forwarderTransport {
	return &forwarderTransport{
		name:       name,
		forwardErr: err,
		status:     core.TransportStatus{Name: name, State: core.StateConnected},
	}
}

func (t *forwarderTransport) Init(ctx context.Context, config core.TransportConfig) error {
	t.name = config.Name
	t.status = core.TransportStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	return nil
}
func (t *forwarderTransport) Start(ctx context.Context) error { return nil }
func (t *forwarderTransport) Stop() error                     { return nil }
func (t *forwarderTransport) Publish(ctx context.Context, point core.DataPoint) error {
	return nil
}
func (t *forwarderTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	return nil
}
func (t *forwarderTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (t *forwarderTransport) OnData() <-chan core.DataPoint       { return nil }
func (t *forwarderTransport) Name() string                        { return t.name }
func (t *forwarderTransport) Type() string                        { return "forwarder" }
func (t *forwarderTransport) Status() core.TransportStatus        { return t.status }

// ForwardCommand records the command and returns the configured error.
func (t *forwarderTransport) ForwardCommand(ctx context.Context, cmd core.WriteCommand) error {
	t.mu.Lock()
	t.forwarded = append(t.forwarded, cmd)
	err := t.forwardErr
	t.mu.Unlock()
	return err
}

func (t *forwarderTransport) forwardedCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.forwarded)
}

func (t *forwarderTransport) lastForwarded() (core.WriteCommand, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.forwarded) == 0 {
		return core.WriteCommand{}, false
	}
	return t.forwarded[len(t.forwarded)-1], true
}

// plainTransport is a core.Transport that does NOT implement
// core.CommandForwarder, used to verify the engine skips non-forwarder
// transports during forwarding.
type plainTransport struct {
	name   string
	status core.TransportStatus
}

func (t *plainTransport) Init(ctx context.Context, config core.TransportConfig) error {
	t.name = config.Name
	t.status = core.TransportStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	return nil
}
func (t *plainTransport) Start(ctx context.Context) error { return nil }
func (t *plainTransport) Stop() error                     { return nil }
func (t *plainTransport) Publish(ctx context.Context, point core.DataPoint) error {
	return nil
}
func (t *plainTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	return nil
}
func (t *plainTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (t *plainTransport) OnData() <-chan core.DataPoint       { return nil }
func (t *plainTransport) Name() string                        { return t.name }
func (t *plainTransport) Type() string                        { return "plain" }
func (t *plainTransport) Status() core.TransportStatus        { return t.status }

// ─── Helpers ────────────────────────────────────────────────────────

// newTestEngine builds a bare CoreCEngine with the maps and lifecycle
// context initialised, ready for direct executeWriteWithRetry calls
// without a full Start(). Tests add drivers/transports to e.drivers /
// e.transports directly.
func newTestEngine(t *testing.T) *CoreCEngine {
	t.Helper()
	e := &CoreCEngine{
		drivers:          make(map[string]core.Driver),
		transports:       make(map[string]core.Transport),
		writeRetryCount:  0, // single attempt: keeps failure tests fast
		deadLetterMaxLen: 1000,
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(e.cancel)
	return e
}

// newRecordingDriver builds the package-shared recordingDriver (defined in
// chained_core_scenarios_test.go) with the given name and a connected
// status, ready to drop into e.drivers. It is reused here rather than
// redefining a recording driver to avoid a duplicate type in the package.
func newRecordingDriver(name string) *recordingDriver {
	d := &recordingDriver{}
	d.name = name
	d.status = core.DriverStatus{Name: name, State: core.StateConnected}
	return d
}

// compile-time assertions that the mocks satisfy the interfaces they claim.
var (
	_ core.Transport        = (*forwarderTransport)(nil)
	_ core.CommandForwarder = (*forwarderTransport)(nil)
	_ core.Transport        = (*plainTransport)(nil)
	_ core.Driver           = (*recordingDriver)(nil)
	_ core.Driver           = (*failingDriver)(nil)
)

// ─── hasDriver / forwardCommand unit tests ──────────────────────────

// TestHasDriver reports whether a driver is registered under the given name.
func TestHasDriver(t *testing.T) {
	e := newTestEngine(t)
	e.drivers["opc"] = newRecordingDriver("opc")

	if !e.hasDriver("opc") {
		t.Error("hasDriver(opc) = false, want true")
	}
	if e.hasDriver("missing") {
		t.Error("hasDriver(missing) = true, want false")
	}
}

// TestForwardCommandReturnsFalseWhenNoForwarder verifies that
// forwardCommand returns false (no forwarding happened) when none of the
// registered transports implement core.CommandForwarder.
func TestForwardCommandReturnsFalseWhenNoForwarder(t *testing.T) {
	e := newTestEngine(t)
	e.transports["plain"] = &plainTransport{name: "plain"}

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 1.0}
	if e.forwardCommand(context.Background(), cmd) {
		t.Fatal("forwardCommand returned true with no forwarder-capable transport")
	}
}

// TestForwardCommandTriesAllForwardersUntilSuccess verifies that
// forwardCommand tries each forwarder-capable transport in order and stops
// at the first success, so a failing first forwarder does not abort the
// chain when a later one succeeds.
func TestForwardCommandTriesAllForwardersUntilSuccess(t *testing.T) {
	e := newTestEngine(t)
	// A plain transport that must be skipped (not a forwarder).
	plain := &plainTransport{name: "plain"}
	// A forwarder that always fails.
	failing := newForwarderTransport("failing", fmt.Errorf("downstream offline"))
	// A forwarder that succeeds.
	ok := newForwarderTransport("ok", nil)
	e.transports["plain"] = plain
	e.transports["failing"] = failing
	e.transports["ok"] = ok

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 2.0}
	if !e.forwardCommand(context.Background(), cmd) {
		t.Fatal("forwardCommand returned false, want true (ok forwarder should have succeeded)")
	}
	if failing.forwardedCount() != 1 {
		t.Errorf("failing forwarder call count: got %d, want 1", failing.forwardedCount())
	}
	if ok.forwardedCount() != 1 {
		t.Errorf("ok forwarder call count: got %d, want 1", ok.forwardedCount())
	}
	if got, okFlag := ok.lastForwarded(); !okFlag || got.Tag != "setpoint" {
		t.Errorf("ok forwarder last command: got %+v (ok=%v), want tag setpoint", got, okFlag)
	}
}

// TestForwardCommandAllFailReturnsFalse verifies that forwardCommand
// returns false when every forwarder-capable transport errors.
func TestForwardCommandAllFailReturnsFalse(t *testing.T) {
	e := newTestEngine(t)
	e.transports["f1"] = newForwarderTransport("f1", fmt.Errorf("err1"))
	e.transports["f2"] = newForwarderTransport("f2", fmt.Errorf("err2"))

	cmd := core.WriteCommand{Driver: "opc", Tag: "t", Value: 1.0}
	if e.forwardCommand(context.Background(), cmd) {
		t.Fatal("forwardCommand returned true, want false (all forwarders failed)")
	}
}

// ─── executeWriteWithRetry integration tests ────────────────────────

// TestExecuteWriteForwardsWhenNoLocalDriver verifies the core
// chained-core passthrough: a relay node with no local driver for the
// command target forwards the command to a downstream forwarder instead
// of dead-lettering it.
func TestExecuteWriteForwardsWhenNoLocalDriver(t *testing.T) {
	e := newTestEngine(t)
	fwd := newForwarderTransport("relay-fwd", nil)
	e.transports["relay-fwd"] = fwd
	// No drivers registered — relay node has drivers: [].

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 42.0, Type: core.TypeFloat64}
	e.executeWriteWithRetry(cmd)

	if fwd.forwardedCount() != 1 {
		t.Fatalf("forwarder call count: got %d, want 1", fwd.forwardedCount())
	}
	if got, ok := fwd.lastForwarded(); !ok || got.Value != 42.0 {
		t.Errorf("forwarded command: got %+v (ok=%v), want value 42.0", got, ok)
	}
	if entries := e.DeadLetterEntries(); len(entries) != 0 {
		t.Errorf("dead letter queue should be empty after successful forward, got %d entries", len(entries))
	}
}

// TestExecuteWriteDeadLettersWhenNoDriverNoForwarder verifies that when a
// relay node has no local driver AND no forwarder-capable transport, the
// command is placed in the dead letter queue with a descriptive error.
func TestExecuteWriteDeadLettersWhenNoDriverNoForwarder(t *testing.T) {
	e := newTestEngine(t)
	// Only a plain (non-forwarder) transport — nothing can forward.
	e.transports["plain"] = &plainTransport{name: "plain"}

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 1.0}
	e.executeWriteWithRetry(cmd)

	if !waitFor(time.Second, func() bool {
		return len(e.DeadLetterEntries()) == 1
	}) {
		t.Fatalf("dead letter queue: got %d entries, want 1", len(e.DeadLetterEntries()))
	}
	entries := e.DeadLetterEntries()
	if !strings.Contains(entries[0].Error, "no forwarder configured") {
		t.Errorf("dead letter error: got %q, want it to contain 'no forwarder configured'", entries[0].Error)
	}
	if entries[0].Attempts != 1 {
		t.Errorf("dead letter attempts: got %d, want 1 (no retries on forward miss)", entries[0].Attempts)
	}
}

// TestExecuteWriteForwarderFailureFallsBackToDeadLetter verifies that
// when the local driver is absent and every forwarder errors, the command
// is dead-lettered rather than silently dropped.
func TestExecuteWriteForwarderFailureFallsBackToDeadLetter(t *testing.T) {
	e := newTestEngine(t)
	e.transports["broken-fwd"] = newForwarderTransport("broken-fwd", fmt.Errorf("downstream unreachable"))

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 1.0}
	e.executeWriteWithRetry(cmd)

	if !waitFor(time.Second, func() bool {
		return len(e.DeadLetterEntries()) == 1
	}) {
		t.Fatalf("dead letter queue: got %d entries, want 1", len(e.DeadLetterEntries()))
	}
	entries := e.DeadLetterEntries()
	if !strings.Contains(entries[0].Error, "no forwarder configured") {
		t.Errorf("dead letter error: got %q, want 'no forwarder configured' (all forwarders failed)", entries[0].Error)
	}
}

// TestExecuteWriteLocalDriverNotForwarded verifies that when a local
// driver matching the command target exists, the command is written
// locally and NOT forwarded to any downstream forwarder.
func TestExecuteWriteLocalDriverNotForwarded(t *testing.T) {
	e := newTestEngine(t)
	drv := newRecordingDriver("opc")
	e.drivers["opc"] = drv
	fwd := newForwarderTransport("relay-fwd", nil)
	e.transports["relay-fwd"] = fwd

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 7.0, Type: core.TypeFloat64}
	e.executeWriteWithRetry(cmd)

	if !waitFor(time.Second, func() bool {
		return len(drv.GetWritten()) == 1
	}) {
		t.Fatalf("driver write count: got %d, want 1", len(drv.GetWritten()))
	}
	if fwd.forwardedCount() != 0 {
		t.Errorf("forwarder call count: got %d, want 0 (local driver must handle the command)", fwd.forwardedCount())
	}
	if entries := e.DeadLetterEntries(); len(entries) != 0 {
		t.Errorf("dead letter queue should be empty after local write, got %d entries", len(entries))
	}
}

// TestExecuteWriteLocalDriverFailureRetriesAndDeadLetters verifies that
// when a local driver exists but its Write fails, the original retry +
// dead-letter path still applies (forwarding is only attempted when no
// local driver matches). Uses failingDriver, defined above, which always
// fails its writes.
func TestExecuteWriteLocalDriverFailureRetriesAndDeadLetters(t *testing.T) {
	e := newTestEngine(t)
	e.writeRetryCount = 1 // 2 attempts total
	drv := &failingDriver{}
	drv.name = "opc"
	e.drivers["opc"] = drv
	fwd := newForwarderTransport("relay-fwd", nil)
	e.transports["relay-fwd"] = fwd

	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 1.0}
	e.executeWriteWithRetry(cmd)

	// 2 write attempts (writeRetryCount+1), never forwarded.
	if !waitFor(time.Second, func() bool {
		return drv.writes() == 2
	}) {
		t.Fatalf("driver write attempts: got %d, want 2", drv.writes())
	}
	if fwd.forwardedCount() != 0 {
		t.Errorf("forwarder call count: got %d, want 0 (local driver exists, must not forward)", fwd.forwardedCount())
	}
	if !waitFor(time.Second, func() bool {
		return len(e.DeadLetterEntries()) == 1
	}) {
		t.Fatalf("dead letter queue: got %d entries, want 1", len(e.DeadLetterEntries()))
	}
	entries := e.DeadLetterEntries()
	if !strings.Contains(entries[0].Error, "device refused write") {
		t.Errorf("dead letter error: got %q, want 'device refused write'", entries[0].Error)
	}
	if entries[0].Attempts != 2 {
		t.Errorf("dead letter attempts: got %d, want 2", entries[0].Attempts)
	}
}

// ─── Type-assertion contract ────────────────────────────────────────

// TestForwarderTransportSatisfiesCommandForwarder is a compile-time guard
// that the mock used here satisfies the new optional capability interface,
// mirroring how the engine type-asserts real transports.
func TestForwarderTransportSatisfiesCommandForwarder(t *testing.T) {
	var tIf core.Transport = &forwarderTransport{name: "x"}
	if _, ok := tIf.(core.CommandForwarder); !ok {
		t.Fatal("forwarderTransport must satisfy core.CommandForwarder")
	}
	var pIf core.Transport = &plainTransport{name: "y"}
	if _, ok := pIf.(core.CommandForwarder); ok {
		t.Fatal("plainTransport must NOT satisfy core.CommandForwarder")
	}
}
