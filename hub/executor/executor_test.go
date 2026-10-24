package executor

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
)

type mockEngineForExecutor struct {
	core.Engine
	addedDrivers   []string
	removedDrivers []string
	rulesSet       []core.RuleConfig
	suspended      bool
	resumed        bool
	derivedApplied [][]core.DerivedTagConfig
}

func (m *mockEngineForExecutor) Suspend() error {
	m.suspended = true
	return nil
}

func (m *mockEngineForExecutor) Resume() error {
	m.resumed = true
	return nil
}

func (m *mockEngineForExecutor) AddDriver(config core.DriverConfig) error {
	m.addedDrivers = append(m.addedDrivers, config.Name)
	return nil
}

func (m *mockEngineForExecutor) RemoveDriver(name string) error {
	m.removedDrivers = append(m.removedDrivers, name)
	return nil
}

func (m *mockEngineForExecutor) AddTransport(config core.TransportConfig) error {
	return nil
}

func (m *mockEngineForExecutor) RemoveTransport(name string) error {
	return nil
}

func (m *mockEngineForExecutor) SetRules(rules []core.RuleConfig) error {
	m.rulesSet = rules
	return nil
}

func TestExecutorApplyConfig(t *testing.T) {
	eng := &mockEngineForExecutor{}

	initialCfg := &core.Config{
		Global: core.GlobalConfig{
			LogLevel: "info",
		},
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "modbus-tcp"},
			{Name: "d2", Type: "s7"},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mqtt"},
		},
		Rules: []core.RuleConfig{
			{Name: "r1", Target: "t1"},
		},
	}

	// Use a temp-dir config path: Reload persists the payload to configPath
	// (and now snapshots the replaced file into <dir>/backups/), so a
	// repo-relative path would write test artifacts into the package
	// directory (see the "config.yaml" cleanup in git history).
	Init(eng, initialCfg, filepath.Join(t.TempDir(), "config.yaml"))

	// Verify current config
	curr := CurrentConfig()
	if curr == nil || len(curr.Drivers) != 2 {
		t.Fatalf("expected 2 drivers in current config")
	}

	// New config: remove d1, keep d2, add d3
	newYAML := `
global:
  log-level: debug
drivers:
  - name: d2
    type: s7
    tags:
      - name: tag1
        address: DB1.DBD0
        type: float32
  - name: d3
    type: opcua
    tags:
      - name: tag2
        address: ns=2;s=Tag2
        type: int32
transports:
  - name: t1
    type: mqtt
rules:
  - name: r2
    match: ALL
    action: forward
    target: t1
`

	err := Reload("", newYAML)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	if !eng.suspended || !eng.resumed {
		t.Errorf("expected engine to be suspended and resumed, got suspended=%v, resumed=%v", eng.suspended, eng.resumed)
	}

	// d1 should be removed
	foundD1Removed := false
	for _, name := range eng.removedDrivers {
		if name == "d1" {
			foundD1Removed = true
			break
		}
	}
	if !foundD1Removed {
		t.Errorf("expected d1 to be removed, removed list: %v", eng.removedDrivers)
	}

	// d3 should be added
	foundD3Added := false
	for _, name := range eng.addedDrivers {
		if name == "d3" {
			foundD3Added = true
			break
		}
	}
	if !foundD3Added {
		t.Errorf("expected d3 to be added, added list: %v", eng.addedDrivers)
	}

	// Rules should be updated
	if len(eng.rulesSet) != 1 || eng.rulesSet[0].Name != "r2" {
		t.Errorf("expected rule r2 set, got: %+v", eng.rulesSet)
	}

	// Test Patch log level
	err = Patch(map[string]any{"log-level": "warn"})
	if err != nil {
		t.Fatalf("Patch failed: %v", err)
	}
	if log.Level() != slog.LevelWarn {
		t.Errorf("expected log level Warn, got %v", log.Level())
	}
}

func (m *mockEngineForExecutor) ApplyDerivedTags(configs []core.DerivedTagConfig) error {
	m.derivedApplied = append(m.derivedApplied, configs)
	return nil
}

// TestApplyConfigHotAppliesDerivedTags pins the reload diff's derived-tags
// step: ApplyConfig must not ignore
// derived-tags changes — PUT returning success while nothing happens
// is a contract violation. The mock implements core.DerivedTagApplier, so the diff
// must forward changed sets to the engine and skip unchanged ones.
func TestApplyConfigHotAppliesDerivedTags(t *testing.T) {
	eng := &mockEngineForExecutor{}
	initial := &core.Config{
		DerivedTags: []core.DerivedTagConfig{
			{Name: "kpi1", Expr: "x * 2", Interval: "5s"},
		},
	}
	Init(eng, initial, filepath.Join(t.TempDir(), "config.yaml"))
	defer Init(nil, nil, "")

	// Same derived set: no apply.
	if err := ApplyConfig(initial, false); err != nil {
		t.Fatalf("ApplyConfig(unchanged): %v", err)
	}
	if len(eng.derivedApplied) != 0 {
		t.Fatalf("unchanged derived tags applied %d times, want 0", len(eng.derivedApplied))
	}

	// Changed derived set: applied exactly once with the new set.
	changed := &core.Config{
		DerivedTags: []core.DerivedTagConfig{
			{Name: "kpi2", Expr: "x * 3", Interval: "5s"},
		},
	}
	if err := ApplyConfig(changed, false); err != nil {
		t.Fatalf("ApplyConfig(changed): %v", err)
	}
	if len(eng.derivedApplied) != 1 {
		t.Fatalf("changed derived tags applied %d times, want 1", len(eng.derivedApplied))
	}
	got := eng.derivedApplied[0]
	if len(got) != 1 || got[0].Name != "kpi2" {
		t.Errorf("applied set = %+v, want [kpi2]", got)
	}

	// force=true reapplies even identical sets.
	if err := ApplyConfig(changed, true); err != nil {
		t.Fatalf("ApplyConfig(force): %v", err)
	}
	if len(eng.derivedApplied) != 2 {
		t.Fatalf("force apply count = %d, want 2", len(eng.derivedApplied))
	}
}
