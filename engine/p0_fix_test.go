package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestMergeFileAndInlineTags(t *testing.T) {
	dc := &core.DriverConfig{Tags: []core.TagConfig{{Name: "inline"}}}
	if err := core.MergeFileAndInlineTags(dc, []core.TagConfig{{Name: "file"}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(dc.Tags) != 2 || dc.Tags[0].Name != "file" || dc.Tags[1].Name != "inline" {
		t.Fatalf("merge order = %+v, want [file inline]", dc.Tags)
	}

	dup := &core.DriverConfig{Tags: []core.TagConfig{{Name: "dup"}}}
	if err := core.MergeFileAndInlineTags(dup, []core.TagConfig{{Name: "dup"}}); err == nil ||
		!strings.Contains(err.Error(), "defined by both") {
		t.Fatalf("duplicate file/inline tag must be rejected, got %v", err)
	}
}

func TestValidateRulesCompilesExpressions(t *testing.T) {
	e := New().(*CoreCEngine)
	if err := e.ValidateRules(&core.Config{Rules: []core.RuleConfig{{
		Name: "bad", Match: "tag ==", Action: "drop",
	}}}); err == nil {
		t.Fatal("ValidateRules must reject an uncompilable match expression")
	}
	if err := e.ValidateRules(&core.Config{Rules: []core.RuleConfig{{
		Name: "ok", Match: "ALL", Action: "drop",
	}}}); err != nil {
		t.Fatalf("ValidateRules rejected a valid rule: %v", err)
	}

	if err := e.ValidateRules(&core.Config{
		RuleGroups: map[string][]core.RuleConfig{
			"grp": {{Name: "inner", Match: "ALL", Action: "drop"}},
		},
		Rules: []core.RuleConfig{{Name: "ref", Match: "SUB-RULE:grp", Action: "drop"}},
	}); err != nil {
		t.Fatalf("ValidateRules rejected a valid SUB-RULE reference: %v", err)
	}
}

// TestPacedCommandWaitsAndSucceeds is the P1-1 regression: pacing is a
// "device busy" outcome, not a permanent failure. With a window longer than
// the normal backoff schedule the command must still reach the device and
// must not be dead-lettered.
func TestPacedCommandWaitsAndSucceeds(t *testing.T) {
	e := newTestEngine(t)
	e.writes.writeRetryCount = 1
	e.writes.pacer.setInterval(120 * time.Millisecond)
	drv := registerTaggedDriver(e, "plc", nil)
	// Simulate a write the device accepted immediately before this command.
	e.writes.pacer.record("plc")

	e.writes.executeWriteWithRetry(core.WriteCommand{
		Driver: "plc", Tag: "t", Value: 1.0, Type: core.TypeFloat32,
	})

	if got := len(e.DeadLetterEntries()); got != 0 {
		t.Fatalf("paced command was dead-lettered: %d entries", got)
	}
	if got := len(drv.GetWritten()); got != 1 {
		t.Fatalf("paced command never reached the device: %d writes", got)
	}
}

// TestStartReplaysDeadLettersWithoutDuplication is the P1-3 regression:
// Start replaces the queue with the JSONL replay instead of appending the
// same entries again on every Stop/Start cycle.
func TestStartReplaysDeadLettersWithoutDuplication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.jsonl")
	e := New().(*CoreCEngine)
	e.writes.deadLetters.maxLen = 10
	store, err := openDeadLetterStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	e.writes.deadLetters.store = store
	e.writes.addDeadLetter(core.DeadLetterEntry{
		Command: core.WriteCommand{Driver: "d", Tag: "t"}, Error: "x", FailedAt: time.Now(),
	})

	cfg := &core.Config{Global: core.GlobalConfig{Engine: core.EngineConfig{DeadLetterPath: path}}}
	ctx := context.Background()
	for cycle := 0; cycle < 2; cycle++ {
		if err := e.Start(ctx, cfg); err != nil {
			t.Fatalf("start %d: %v", cycle+1, err)
		}
		if got := len(e.DeadLetterEntries()); got != 1 {
			t.Fatalf("after start %d: queue = %d, want 1 (replay must replace, not append)", cycle+1, got)
		}
		if err := e.Stop(); err != nil {
			t.Fatalf("stop %d: %v", cycle+1, err)
		}
	}
}

// TestRemovingDeadLetterPathClearsStore is the P2-5 regression: removing
// dead-letter-path must close and nil the store, not leave a pointer to a
// closed descriptor.
func TestRemovingDeadLetterPathClearsStore(t *testing.T) {
	e := newTestEngine(t)
	path := filepath.Join(t.TempDir(), "dlq.jsonl")
	if err := e.writes.applyConfig(&core.EngineConfig{DeadLetterPath: path}); err != nil {
		t.Fatalf("enable store: %v", err)
	}
	if e.writes.deadLetters.store == nil {
		t.Fatal("store was not opened")
	}
	if err := e.writes.applyConfig(&core.EngineConfig{}); err != nil {
		t.Fatalf("disable store: %v", err)
	}
	if e.writes.deadLetters.store != nil {
		t.Fatal("store still points at a closed file after the path was removed")
	}
}
