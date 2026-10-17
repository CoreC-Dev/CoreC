package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/rule"
)

func TestParseBadQualityPolicy(t *testing.T) {
	tests := []struct {
		input   string
		want    badQualityPolicy
		wantErr bool
	}{
		{"", badQualityPublish, false},
		{"publish", badQualityPublish, false},
		{"drop", badQualityDrop, false},
		{"mark-and-publish", badQualityMarkAndPublish, false},
		{"alert", badQualityAlert, false},
		{"PUBLISH", badQualityPublish, false}, // case-insensitive
		{"Drop", badQualityDrop, false},       // case-insensitive
		{"unknown", badQualityPublish, true},  // invalid → error
		{"pubish", badQualityPublish, true},   // typo → error
	}
	for _, tc := range tests {
		got, err := parseBadQualityPolicy(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseBadQualityPolicy(%q) expected error, got nil", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBadQualityPolicy(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseBadQualityPolicy(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestDeadLetterQueue(t *testing.T) {
	e := newEngineShell()
	e.writes.deadLetterMaxLen = 1000

	cmd := core.WriteCommand{Driver: "plc1", Tag: "setpoint", Value: 50.0}

	// Add entries
	for i := 0; i < 5; i++ {
		e.writes.addDeadLetter(core.DeadLetterEntry{
			Command:  cmd,
			Error:    "connection refused",
			Attempts: i + 1,
		})
	}

	entries := e.DeadLetterEntries()
	if len(entries) != 5 {
		t.Fatalf("len(entries) = %d, want 5", len(entries))
	}

	// Verify first entry
	if entries[0].Error != "connection refused" {
		t.Errorf("entries[0].Error = %q, want %q", entries[0].Error, "connection refused")
	}
	if entries[0].Attempts != 1 {
		t.Errorf("entries[0].Attempts = %d, want 1", entries[0].Attempts)
	}
	if entries[0].Command.Tag != "setpoint" {
		t.Errorf("entries[0].Command.Tag = %q, want %q", entries[0].Command.Tag, "setpoint")
	}
}

func TestDeadLetterQueueEviction(t *testing.T) {
	e := newEngineShell()
	e.writes.deadLetterMaxLen = 3 // small capacity

	cmd := core.WriteCommand{Driver: "plc1", Tag: "setpoint", Value: 50.0}

	// Add 5 entries, capacity is 3
	for i := 0; i < 5; i++ {
		e.writes.addDeadLetter(core.DeadLetterEntry{
			Command:  cmd,
			Error:    "error",
			Attempts: i + 1,
		})
	}

	entries := e.DeadLetterEntries()
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (after eviction)", len(entries))
	}

	// Should keep the 3 newest (attempts 3, 4, 5)
	if entries[0].Attempts != 3 {
		t.Errorf("entries[0].Attempts = %d, want 3", entries[0].Attempts)
	}
	if entries[2].Attempts != 5 {
		t.Errorf("entries[2].Attempts = %d, want 5", entries[2].Attempts)
	}
}

func TestDeadLetterQueueEmpty(t *testing.T) {
	e := newEngineShell()
	e.writes.deadLetterMaxLen = 1000

	entries := e.DeadLetterEntries()
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}

// TestBadQualityAlertPolicyFiresWithoutRuleMatch is the stage-B audit
// finding B5-1 regression test: on-bad-quality: alert must trigger the
// alert callback even when NO configured rule matches the bad point
// (previously the alert sat behind rule matching and Match()==0
// returned early, so a zero-rule config silently never alerted,
// violating the documented contract in docs/config/global.md).
func TestBadQualityAlertPolicyFiresWithoutRuleMatch(t *testing.T) {
	e := New().(*CoreCEngine)
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		e.cancel()
		e.wg.Wait()
	})
	e.dataBus = NewDataBus(4096)
	e.cache = NewLatestCache()
	e.ruleSet.ruleEngine = rule.NewEngine() // zero rules: the B5-1 minimal repro
	e.badQualityPolicy = badQualityAlert

	type alertEvent struct {
		point core.DataPoint
		rule  core.Rule
	}
	alerts := make(chan alertEvent, 4)
	e.OnAlert(func(p core.DataPoint, r core.Rule) {
		alerts <- alertEvent{point: p, rule: r}
	})

	e.processPoint(core.DataPoint{
		Driver: "plc", Tag: "temp", Value: nil,
		Quality:   core.QualityBad,
		Timestamp: time.Now(),
	})

	select {
	case a := <-alerts:
		if a.rule == nil {
			t.Fatal("policy alert must carry a rule context")
		}
		if a.rule.Name() != "bad-quality-policy" {
			t.Errorf("policy alert rule name = %q, want %q", a.rule.Name(), "bad-quality-policy")
		}
		if a.point.Quality != core.QualityBad {
			t.Errorf("alerted point quality = %v, want QualityBad", a.point.Quality)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("on-bad-quality: alert policy never fired — zero rules must not silence the policy alert (B5-1)")
	}
}

// TestBadQualityAlertPolicyNoDuplicateWithAlertRule verifies the
// anti-double-alert behavior is preserved: when the policy fires and a
// matching action=alert rule also fires, the handler is called exactly
// once for the point.
func TestBadQualityAlertPolicyNoDuplicateWithAlertRule(t *testing.T) {
	e := New().(*CoreCEngine)
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		e.cancel()
		e.wg.Wait()
	})
	e.dataBus = NewDataBus(4096)
	e.cache = NewLatestCache()
	e.ruleSet.ruleEngine = rule.NewEngine()
	e.badQualityPolicy = badQualityAlert

	if err := e.ruleSet.ruleEngine.SetRules([]core.RuleConfig{
		{Name: "alert-on-bad", Match: "quality == 'bad'", Action: "alert", Priority: 1},
	}); err != nil {
		t.Fatalf("SetRules: %v", err)
	}

	var alerts atomic.Int32
	e.OnAlert(func(core.DataPoint, core.Rule) { alerts.Add(1) })

	e.processPoint(core.DataPoint{
		Driver: "plc", Tag: "temp", Value: nil,
		Quality:   core.QualityBad,
		Timestamp: time.Now(),
	})

	if got := alerts.Load(); got != 1 {
		t.Errorf("alert handler called %d times for one bad point, want exactly 1 (policy + rule must not double-fire)", got)
	}
}
