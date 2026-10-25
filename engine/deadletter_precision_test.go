package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestDeadLetterStore64BitRoundTrip guards the persisted dead-letter
// replay: a failed 64-bit write command must survive the on-disk JSONL
// round-trip bit-exactly. The default `any` decode coerces numbers to
// float64 (9007199254740993 → 9007199254740992), which corrupted
// commands on every crash-restart replay — same class as the offline
// buffer. The WriteCommand.UnmarshalJSON type-aware decode restores
// the exact value.
func TestDeadLetterStore64BitRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deadletter.jsonl")

	st, err := newDeadLetterStore(path, 1<<20)
	if err != nil {
		t.Fatalf("newDeadLetterStore: %v", err)
	}
	entry := core.DeadLetterEntry{
		Command: core.WriteCommand{
			Driver: "plc",
			Tag:    "counter64",
			Value:  int64(9007199254740993),
			Type:   core.TypeInt64,
		},
		Error:     "device rejected",
		FailedAt:  time.Now(),
		Attempts:  0,
		Transport: "mqtt",
	}
	st.append(entry)
	st.close() // flush + fsync

	// Simulate a restart: a fresh store replays the persisted file.
	st2, err := newDeadLetterStore(path, 1<<20)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.close()
	var replayed []core.DeadLetterEntry
	st2.load(100, func(e core.DeadLetterEntry) {
		replayed = append(replayed, e)
	})
	if len(replayed) != 1 {
		t.Fatalf("expected 1 replayed entry, got %d", len(replayed))
	}
	got, ok := replayed[0].Command.Value.(int64)
	if !ok {
		t.Fatalf("expected int64, got %T (%v)", replayed[0].Command.Value, replayed[0].Command.Value)
	}
	if got != 9007199254740993 {
		t.Fatalf("silently corrupted on DLQ replay: persisted 9007199254740993, got %d", got)
	}
	if replayed[0].Command.Type != core.TypeInt64 || replayed[0].Command.Tag != "counter64" {
		t.Fatalf("command metadata lost: %+v", replayed[0].Command)
	}
}
