package engine

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestParseBadQualityPolicy(t *testing.T) {
	tests := []struct {
		input string
		want  badQualityPolicy
	}{
		{"", badQualityPublish},
		{"publish", badQualityPublish},
		{"drop", badQualityDrop},
		{"mark-and-publish", badQualityMarkAndPublish},
		{"alert", badQualityAlert},
		{"unknown", badQualityPublish}, // unknown defaults to publish
		{"PUBLISH", badQualityPublish}, // case-sensitive, unknown defaults to publish
	}
	for _, tc := range tests {
		got := parseBadQualityPolicy(tc.input)
		if got != tc.want {
			t.Errorf("parseBadQualityPolicy(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestDeadLetterQueue(t *testing.T) {
	e := &CoreCEngine{
		deadLetterMaxLen: 1000,
	}

	cmd := core.WriteCommand{Driver: "plc1", Tag: "setpoint", Value: 50.0}

	// Add entries
	for i := 0; i < 5; i++ {
		e.addDeadLetter(core.DeadLetterEntry{
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
	e := &CoreCEngine{
		deadLetterMaxLen: 3, // small capacity
	}

	cmd := core.WriteCommand{Driver: "plc1", Tag: "setpoint", Value: 50.0}

	// Add 5 entries, capacity is 3
	for i := 0; i < 5; i++ {
		e.addDeadLetter(core.DeadLetterEntry{
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
	e := &CoreCEngine{
		deadLetterMaxLen: 1000,
	}

	entries := e.DeadLetterEntries()
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}
