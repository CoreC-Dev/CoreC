package engine

import (
	"context"
	"os"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestRemoveTransportCleansBatcher verifies that RemoveTransport stops
// the associated batcher and removes it from the batchers map (problem 1+2).
// Before the fix, RemoveTransport only deleted the transport but left the
// batcher running, causing a goroutine leak and routing data to a stopped
// transport.
func TestRemoveTransportCleansBatcher(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{
			{
				Name:          "batched-transport",
				Type:          "mock-data",
				BatchSize:     10,
				FlushInterval: "100ms",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	e := eng.(*CoreCEngine)

	// Verify batcher exists before removal
	e.mu.RLock()
	_, hasBatcher := e.batchers["batched-transport"]
	e.mu.RUnlock()
	if !hasBatcher {
		t.Fatal("expected batcher to exist before removal")
	}

	// Remove the transport
	if err := eng.RemoveTransport("batched-transport"); err != nil {
		t.Fatalf("RemoveTransport failed: %v", err)
	}

	// Verify batcher is removed from the map
	e.mu.RLock()
	_, hasBatcher = e.batchers["batched-transport"]
	e.mu.RUnlock()
	if hasBatcher {
		t.Error("BUG: batcher still exists in map after RemoveTransport — should be cleaned up")
	}
}

// TestRemoveDriverCleansSchedulerTasks verifies that RemoveDriver removes
// scheduler tasks for the driver, not just pausing them (problem 3).
// Before the fix, RemoveDriver only called PauseDriver which left task
// goroutines running forever.
func TestRemoveDriverCleansSchedulerTasks(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "removable-driver", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	e := eng.(*CoreCEngine)

	// Verify task exists before removal
	taskID := "removable-driver_100ms"
	e.scheduler.mu.RLock()
	_, hasTask := e.scheduler.tasks[taskID]
	taskCount := len(e.scheduler.tasks)
	e.scheduler.mu.RUnlock()
	if !hasTask {
		t.Fatal("expected task to exist before driver removal")
	}

	// Remove the driver
	if err := eng.RemoveDriver("removable-driver"); err != nil {
		t.Fatalf("RemoveDriver failed: %v", err)
	}

	// Verify task is removed from the scheduler
	e.scheduler.mu.RLock()
	_, hasTask = e.scheduler.tasks[taskID]
	newTaskCount := len(e.scheduler.tasks)
	e.scheduler.mu.RUnlock()
	if hasTask {
		t.Error("BUG: task still exists in scheduler after RemoveDriver — should be cleaned up")
	}
	if newTaskCount >= taskCount {
		t.Errorf("expected task count to decrease after removal, got before=%d after=%d", taskCount, newTaskCount)
	}
}

// TestReloadClosesRuleProviders verifies that Reload (Stop→Start) closes
// old rule providers so their reload-loop goroutines don't leak (problem 4).
func TestReloadClosesRuleProviders(t *testing.T) {
	registerMockTypes()

	// Create a temporary rule provider file
	tmpDir := t.TempDir()
	ruleFile := tmpDir + "/rules.yaml"
	if err := os.WriteFile(ruleFile, []byte("rules:\n  - name: r1\n    match: ALL\n    action: forward\n"), 0o644); err != nil {
		t.Fatalf("failed to write rule file: %v", err)
	}

	eng := New()
	cfg1 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		RuleProviders: []core.RuleProviderConfig{
			{Name: "dynamic", Type: "file", Path: ruleFile, Interval: "100ms"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg1); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	e := eng.(*CoreCEngine)

	// Verify provider exists
	if count := e.ruleEngine.ProviderCount(); count != 1 {
		t.Fatalf("expected 1 provider, got %d", count)
	}

	// Reload with a config that has no rule providers
	cfg2 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d2", Type: "mock-state", Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Verify old providers are closed and removed
	if count := e.ruleEngine.ProviderCount(); count != 0 {
		t.Errorf("expected 0 providers after reload with no providers, got %d", count)
	}
}

// TestDiffTransportsModifyCleansBatcher verifies that modifying a transport
// via RemoveTransport + AddTransport properly cleans up the old batcher
// when batch settings are removed (problem 2).
func TestDiffTransportsModifyCleansBatcher(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg1 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{
			{
				Name:          "t1",
				Type:          "mock-data",
				BatchSize:     10,
				FlushInterval: "100ms",
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg1); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Modify the transport: remove batch settings
	if err := eng.RemoveTransport("t1"); err != nil {
		t.Fatalf("RemoveTransport failed: %v", err)
	}
	if err := eng.AddTransport(core.TransportConfig{
		Name: "t1",
		Type: "mock-data",
	}); err != nil {
		t.Fatalf("AddTransport failed: %v", err)
	}

	e := eng.(*CoreCEngine)

	// Verify no stale batcher remains
	e.mu.RLock()
	_, hasBatcher := e.batchers["t1"]
	e.mu.RUnlock()
	if hasBatcher {
		t.Error("BUG: stale batcher exists after modifying transport to remove batch settings — should be cleaned up")
	}
}
