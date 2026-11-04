package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// auditEngine builds a bare engine with an audit store at path and a driver
// that accepts writes.
func auditEngine(t *testing.T, path string) *CoreCEngine {
	t.Helper()
	e := newTestEngine(t)
	e.driverReg.drivers["plc"] = newRecordingDriver("plc")
	if err := e.writes.applyConfig(&core.EngineConfig{WriteAuditPath: path}); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	if e.writes.writeAudits.store == nil {
		t.Fatal("audit store should be open")
	}
	t.Cleanup(func() { e.writes.writeAudits.close() })
	return e
}

// TestWriteAuditStoreSurvivesRestart: the audit trail must still answer
// "who wrote what" after a process restart, and the newest entries replay
// into the in-memory ring.
func TestWriteAuditStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ctx := context.Background()

	e := auditEngine(t, path)
	for i := 0; i < 2; i++ {
		if _, err := e.WriteTag(ctx, core.WriteCommand{Driver: "plc", Tag: "t1", Value: float64(i), Type: core.TypeFloat32}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	e.writes.writeAudits.close()

	// Simulate a restart with the same path.
	e2 := auditEngine(t, path)
	hist := e2.WriteHistory()
	if len(hist) < 2 {
		t.Fatalf("replayed history = %d entries, want >= 2", len(hist))
	}
}

// TestWriteAuditStoreEmptyPathKeepsMemoryOnly pins the default: no path
// configured means no file and no behavioural change.
func TestWriteAuditStoreEmptyPathKeepsMemoryOnly(t *testing.T) {
	e := newTestEngine(t)
	e.driverReg.drivers["plc"] = newRecordingDriver("plc")
	if err := e.writes.applyConfig(&core.EngineConfig{}); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	if e.writes.writeAudits.store != nil {
		t.Fatal("audit store must stay nil without write-audit-path")
	}
	if _, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "plc", Tag: "t1", Value: 1.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(e.writes.WriteHistory()) != 1 {
		t.Errorf("in-memory audit history should still record the write")
	}
}

// TestWriteAuditStoreSkipsTornLines: a torn final line (crash during append)
// must not block the replay of the lines before it.
func TestWriteAuditStoreSkipsTornLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	e := auditEngine(t, path)
	if _, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "plc", Tag: "t1", Value: 1.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("write: %v", err)
	}
	e.writes.writeAudits.close()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{\"timestamp\": \"2026-\"\n")
	_ = f.Close()

	e2 := auditEngine(t, path)
	if got := len(e2.WriteHistory()); got != 1 {
		t.Fatalf("replayed %d entries around a torn line, want 1", got)
	}
}
