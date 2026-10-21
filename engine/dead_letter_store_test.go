package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func dlEntry(tag string, at time.Time) core.DeadLetterEntry {
	return core.DeadLetterEntry{
		Command:  core.WriteCommand{Driver: "plc", Tag: tag, Value: 1, Type: core.TypeInt32},
		Error:    "boom",
		FailedAt: at,
		Attempts: 3,
	}
}

// TestDeadLetterStoreAppendLoadRoundTrip verifies the RES-2 persistence:
// entries appended before a "restart" replay into the queue after.
func TestDeadLetterStoreAppendLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dl", "deadletters.jsonl")
	store, err := newDeadLetterStore(path, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Now()
	for i, tag := range []string{"t1", "t2", "t3"} {
		store.append(dlEntry(tag, now.Add(time.Duration(i)*time.Second)))
	}
	store.close()

	// Fresh store instance simulating the process restart.
	reopened, err := newDeadLetterStore(path, 0)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.close()

	var loaded []core.DeadLetterEntry
	reopened.load(10, func(e core.DeadLetterEntry) { loaded = append(loaded, e) })

	if len(loaded) != 3 {
		t.Fatalf("replayed %d entries, want 3", len(loaded))
	}
	// File order preserved: t1 first, t3 last (newest last mirrors the queue's append order).
	if loaded[0].Command.Tag != "t1" || loaded[2].Command.Tag != "t3" {
		t.Errorf("replay order = %v,%v,%v", loaded[0].Command.Tag, loaded[1].Command.Tag, loaded[2].Command.Tag)
	}
}

// TestDeadLetterStoreCapKeepsNewest verifies load caps at the queue bound,
// keeping the newest entries.
func TestDeadLetterStoreCapKeepsNewest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deadletters.jsonl")
	store, err := newDeadLetterStore(path, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	base := time.Now()
	for i := 0; i < 5; i++ {
		store.append(dlEntry(string(rune('a'+i)), base.Add(time.Duration(i)*time.Second)))
	}
	store.close()

	reopened, err := newDeadLetterStore(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.close()

	var loaded []core.DeadLetterEntry
	reopened.load(2, func(e core.DeadLetterEntry) { loaded = append(loaded, e) })
	if len(loaded) != 2 || loaded[0].Command.Tag != "d" || loaded[1].Command.Tag != "e" {
		t.Errorf("capped replay = %+v, want the newest two [d e]", loaded)
	}
}

// TestDeadLetterStoreTornLineSkipped verifies crash-safety: a torn
// trailing line (crash mid-append) is skipped without losing prior entries.
func TestDeadLetterStoreTornLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deadletters.jsonl")
	store, err := newDeadLetterStore(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.append(dlEntry("good-1", time.Now()))
	store.append(dlEntry("good-2", time.Now()))
	store.close()

	// Simulate the crash: append a torn line directly.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"command":{"driver":"plc","tag":"tor`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	reopened, _ := newDeadLetterStore(path, 0)
	defer reopened.close()
	var loaded []core.DeadLetterEntry
	reopened.load(10, func(e core.DeadLetterEntry) { loaded = append(loaded, e) })
	if len(loaded) != 2 {
		t.Fatalf("replayed %d entries after torn line, want 2 (torn line skipped)", len(loaded))
	}
}

// TestDeadLetterStoreRotationBoundsDisk verifies the byte cap rotates to
// .old and replay reads both generations. The bound is two generations
// by design (bounded disk = bounded history): entries older than the
// last rotation are dropped, exactly like any rotating log — so the
// test sizes the cap for exactly ONE rotation and asserts the full
// two-generation window replays.
func TestDeadLetterStoreRotationBoundsDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deadletters.jsonl")
	store, err := newDeadLetterStore(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Size probe: append one representative entry, close, stat.
	bigTag := strings.Repeat("t", 200) // deterministic ~250B line
	store.append(dlEntry(bigTag, time.Now()))
	store.close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	lineSize := info.Size()

	// Reopen with a cap of three lines: appends rotate every ~3rd
	// entry. Four entries ⇒ exactly one rotation (.old = first
	// generation, current = the rest).
	store, err = newDeadLetterStore(path, 3*lineSize+32)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	base := time.Now()
	for i := 0; i < 4; i++ {
		store.append(dlEntry(string(bigTag)+string(rune('a'+i)), base))
	}
	store.close()

	if _, err := os.Stat(path + ".old"); err != nil {
		t.Fatalf("rotation did not happen: %v", err)
	}
	reopened, _ := newDeadLetterStore(path, 3*lineSize+32)
	defer reopened.close()
	var loaded []core.DeadLetterEntry
	reopened.load(1000, func(e core.DeadLetterEntry) { loaded = append(loaded, e) })
	if len(loaded) < 4 {
		t.Errorf("replayed %d entries across both generations, want ≥4 (the full recent window)", len(loaded))
	}
}

// TestDeadLetterStoreDisabledIsNil verifies the empty-path default
// keeps the historical memory-only behaviour: a nil store with an
// explicit errDeadLetterDisabled sentinel (nilnil lint rule — the
// signature never returns nil,nil), and the nil store's methods are
// all no-ops.
func TestDeadLetterStoreDisabledIsNil(t *testing.T) {
	st, err := newDeadLetterStore("", 0)
	if st != nil {
		t.Errorf("empty path: store=%v, want nil (disabled)", st)
	}
	if !errors.Is(err, errDeadLetterDisabled) {
		t.Errorf("empty path: err=%v, want errDeadLetterDisabled", err)
	}
	// The nil store's methods must be no-ops (addDeadLetter calls them).
	st.append(dlEntry("x", time.Now()))
	st.close()
	st.load(10, func(core.DeadLetterEntry) { t.Error("load on nil store must not replay") })
}
