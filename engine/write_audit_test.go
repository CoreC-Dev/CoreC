package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// The write audit history (GET /write/history) records every WriteTag
// attempt — success and failure alike — in a bounded, newest-first
// buffer. These tests reuse the in-package mocks (recordingDriver,
// failingDriver, mockTransport) and the newTestEngine helper from
// command_forward_test.go; they need no external broker or device and
// must never be skipped.

func TestWriteTagAuditsSuccessfulWrite(t *testing.T) {
	e := newTestEngine(t)
	e.driverReg.drivers["plc"] = newRecordingDriver("plc")

	res, err := e.WriteTag(context.Background(), core.WriteCommand{
		Driver: "plc",
		Tag:    "setpoint",
		Value:  42.0,
		Type:   core.TypeFloat64,
		Source: "api",
		Remote: "127.0.0.1:1234",
	})
	if err != nil || !res.Success {
		t.Fatalf("WriteTag: res=%+v err=%v", res, err)
	}

	entries := e.WriteHistory()
	if len(entries) != 1 {
		t.Fatalf("write history: got %d entries, want 1", len(entries))
	}
	got := entries[0]
	if !got.Success || got.Error != "" {
		t.Errorf("entry success/error: got %v/%q, want true/\"\"", got.Success, got.Error)
	}
	if got.Driver != "plc" || got.Tag != "setpoint" || got.Value != 42.0 {
		t.Errorf("entry command fields: got %+v", got)
	}
	if got.Type != core.TypeFloat64 {
		t.Errorf("entry type: got %v, want float64", got.Type)
	}
	if got.Source != "api" || got.Remote != "127.0.0.1:1234" {
		t.Errorf("entry provenance: got source=%q remote=%q, want api/127.0.0.1:1234", got.Source, got.Remote)
	}
	if got.Timestamp.IsZero() {
		t.Error("entry timestamp is zero; want the write time")
	}
}

func TestWriteTagAuditsFailedWrite(t *testing.T) {
	e := newTestEngine(t)
	drv := &failingDriver{}
	drv.name = "plc"
	e.driverReg.drivers["plc"] = drv

	res, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "plc", Tag: "setpoint", Value: 1})
	if err != nil || res == nil {
		t.Fatalf("WriteTag: res=%+v err=%v", res, err)
	}
	if res.Success {
		t.Fatal("failingDriver reported success")
	}

	entries := e.WriteHistory()
	if len(entries) != 1 {
		t.Fatalf("write history: got %d entries, want 1", len(entries))
	}
	if entries[0].Success {
		t.Error("entry success=true, want false for a failed write")
	}
	if entries[0].Error == "" {
		t.Error("entry error is empty; want the driver-reported failure message")
	}
}

func TestWriteTagAuditsDriverNotFound(t *testing.T) {
	e := newTestEngine(t)

	if _, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "missing", Tag: "t"}); err == nil {
		t.Fatal("WriteTag with unknown driver returned nil error")
	}
	entries := e.WriteHistory()
	if len(entries) != 1 {
		t.Fatalf("write history: got %d entries, want 1 (failed attempts are audited too)", len(entries))
	}
	if entries[0].Success || entries[0].Error == "" {
		t.Errorf("entry: got success=%v error=%q, want false with a message", entries[0].Success, entries[0].Error)
	}
}

func TestWriteHistoryEmptyIsNonNil(t *testing.T) {
	e := newTestEngine(t)
	entries := e.WriteHistory()
	if entries == nil {
		t.Fatal("WriteHistory returned nil; want a non-nil empty slice so JSON serializes as [] not null")
	}
	if len(entries) != 0 {
		t.Fatalf("fresh engine: got %d entries, want 0", len(entries))
	}
}

func TestWriteHistoryNewestFirstAndRingEviction(t *testing.T) {
	e := newTestEngine(t)
	e.driverReg.drivers["plc"] = newRecordingDriver("plc")

	const total = writeAuditCapacity + 5
	for i := 0; i < total; i++ {
		cmd := core.WriteCommand{Driver: "plc", Tag: fmt.Sprintf("tag-%d", i), Value: i}
		if _, err := e.WriteTag(context.Background(), cmd); err != nil {
			t.Fatalf("WriteTag %d failed: %v", i, err)
		}
	}

	entries := e.WriteHistory()
	if len(entries) != writeAuditCapacity {
		t.Fatalf("write history length: got %d, want %d (ring eviction of the oldest entries)", len(entries), writeAuditCapacity)
	}
	// Newest first: the most recent write must be at index 0.
	if got, want := entries[0].Tag, fmt.Sprintf("tag-%d", total-1); got != want {
		t.Errorf("newest entry: got %q, want %q", got, want)
	}
	// The oldest surviving entry is the (total-capacity)-th write; the
	// first `overflow` writes must have been evicted.
	if got, want := entries[len(entries)-1].Tag, fmt.Sprintf("tag-%d", total-writeAuditCapacity); got != want {
		t.Errorf("oldest surviving entry: got %q, want %q", got, want)
	}
	// Strictly decreasing counters prove the newest-first ordering.
	for i := 1; i < len(entries); i++ {
		prev, _ := entries[i-1].Value.(int)
		cur, _ := entries[i].Value.(int)
		if cur != prev-1 {
			t.Fatalf("ordering broken at index %d: got %d after %d", i, cur, prev)
		}
	}
}

func TestCommandListenerStampsAuditSource(t *testing.T) {
	e := newTestEngine(t)
	e.writeRetryCount = 0 // single attempt — keeps the test fast
	e.commandSem = make(chan struct{}, 1)

	drv := newRecordingDriver("plc")
	e.driverReg.drivers["plc"] = drv

	tr := &mockTransport{name: "cmd-tr"}
	tr.cmdCh = make(chan core.WriteCommand, 1)
	tr.status = core.TransportStatus{Name: "cmd-tr", State: core.StateConnected}

	e.startCommandListener(tr, e.ctx)
	t.Cleanup(func() {
		e.cancel()
		e.wg.Wait()
	})

	// A command payload carrying a spoofed "source" field must still be
	// recorded as a command-path write: the engine stamps the provenance
	// at the trust boundary.
	tr.cmdCh <- core.WriteCommand{Driver: "plc", Tag: "setpoint", Value: 1, Source: "api"}

	if !waitFor(time.Second, func() bool { return len(drv.GetWritten()) == 1 }) {
		t.Fatal("command was not dispatched to the driver")
	}
	entries := e.WriteHistory()
	if len(entries) != 1 {
		t.Fatalf("write history: got %d entries, want 1", len(entries))
	}
	if entries[0].Source != "command" {
		t.Errorf("audit source: got %q, want %q (stamped by the command listener, not the payload)", entries[0].Source, "command")
	}
	if entries[0].Remote != "" {
		t.Errorf("audit remote: got %q, want empty for command-path writes", entries[0].Remote)
	}
}
