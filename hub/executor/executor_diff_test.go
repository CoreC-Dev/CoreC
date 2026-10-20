package executor

import (
	"errors"
	"reflect"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// mockEngineRemoveFail is a mock engine whose RemoveDriver/RemoveTransport
// return removeErr when set, allowing tests to verify error propagation
// from diffDrivers/diffTransports.
type mockEngineRemoveFail struct {
	core.Engine
	removeDriverErr    error
	removeTransportErr error
	removedDrivers     []string
	removedTransports  []string
}

func (m *mockEngineRemoveFail) AddDriver(core.DriverConfig) error { return nil }
func (m *mockEngineRemoveFail) RemoveDriver(name string) error {
	m.removedDrivers = append(m.removedDrivers, name)
	return m.removeDriverErr
}
func (m *mockEngineRemoveFail) AddTransport(core.TransportConfig) error { return nil }
func (m *mockEngineRemoveFail) RemoveTransport(name string) error {
	m.removedTransports = append(m.removedTransports, name)
	return m.removeTransportErr
}
func (m *mockEngineRemoveFail) SetRules([]core.RuleConfig) error { return nil }
func (m *mockEngineRemoveFail) Suspend() error                   { return nil }
func (m *mockEngineRemoveFail) Resume() error                    { return nil }

// TestDiffDrivers_RemoveAllToEmpty verifies the N→0 reload path: removing
// all drivers when the new config has none. This was previously untested.
func TestDiffDrivers_RemoveAllToEmpty(t *testing.T) {
	eng := &mockEngineRemoveFail{}
	prevEngine := engine
	engine = eng
	defer func() { engine = prevEngine }()

	oldDrivers := []core.DriverConfig{
		{Name: "d1", Type: "modbus-tcp"},
		{Name: "d2", Type: "s7"},
	}
	// New config has zero drivers — all should be removed.
	err := diffDrivers(oldDrivers, nil, false, &diffActions{})
	if err != nil {
		t.Fatalf("expected nil error for successful removal, got: %v", err)
	}
	if len(eng.removedDrivers) != 2 {
		t.Errorf("expected 2 drivers removed, got %d: %v", len(eng.removedDrivers), eng.removedDrivers)
	}
}

// TestDiffDrivers_RemoveErrorPropagated verifies that a RemoveDriver failure
// is now propagated as an error (previously swallowed with slog.Error).
func TestDiffDrivers_RemoveErrorPropagated(t *testing.T) {
	eng := &mockEngineRemoveFail{
		removeDriverErr: errors.New("driver busy"),
	}
	prevEngine := engine
	engine = eng
	defer func() { engine = prevEngine }()

	oldDrivers := []core.DriverConfig{
		{Name: "d1", Type: "modbus-tcp"},
	}
	err := diffDrivers(oldDrivers, nil, false, &diffActions{})
	if err == nil {
		t.Fatal("expected error from failed driver removal, got nil")
	}
}

// TestDiffDrivers_RestartRemoveErrorPropagated verifies that a RemoveDriver
// failure during a restart (modified driver) is propagated.
func TestDiffDrivers_RestartRemoveErrorPropagated(t *testing.T) {
	eng := &mockEngineRemoveFail{
		removeDriverErr: errors.New("cannot stop"),
	}
	prevEngine := engine
	engine = eng
	defer func() { engine = prevEngine }()

	oldDrivers := []core.DriverConfig{
		{Name: "d1", Type: "modbus-tcp"},
	}
	newDrivers := []core.DriverConfig{
		{Name: "d1", Type: "opcua"}, // same name, different type → restart
	}
	err := diffDrivers(oldDrivers, newDrivers, false, &diffActions{})
	if err == nil {
		t.Fatal("expected error from failed driver stop during restart, got nil")
	}
}

// TestDiffTransports_RemoveAllToEmpty verifies the N→0 reload path for
// transports: removing all transports when the new config has none.
func TestDiffTransports_RemoveAllToEmpty(t *testing.T) {
	eng := &mockEngineRemoveFail{}
	prevEngine := engine
	engine = eng
	defer func() { engine = prevEngine }()

	oldTransports := []core.TransportConfig{
		{Name: "t1", Type: "mqtt"},
		{Name: "t2", Type: "http-push"},
	}
	// New config has zero transports — all should be removed.
	err := diffTransports(oldTransports, nil, false, &diffActions{})
	if err != nil {
		t.Fatalf("expected nil error for successful removal, got: %v", err)
	}
	if len(eng.removedTransports) != 2 {
		t.Errorf("expected 2 transports removed, got %d: %v", len(eng.removedTransports), eng.removedTransports)
	}
}

// TestDiffTransports_RemoveErrorPropagated verifies that a RemoveTransport
// failure is now propagated as an error (previously swallowed with slog.Error).
func TestDiffTransports_RemoveErrorPropagated(t *testing.T) {
	eng := &mockEngineRemoveFail{
		removeTransportErr: errors.New("transport busy"),
	}
	prevEngine := engine
	engine = eng
	defer func() { engine = prevEngine }()

	oldTransports := []core.TransportConfig{
		{Name: "t1", Type: "mqtt"},
	}
	err := diffTransports(oldTransports, nil, false, &diffActions{})
	if err == nil {
		t.Fatal("expected error from failed transport removal, got nil")
	}
}

// TestDiffTransports_RestartRemoveErrorPropagated verifies that a
// RemoveTransport failure during a restart is propagated.
func TestDiffTransports_RestartRemoveErrorPropagated(t *testing.T) {
	eng := &mockEngineRemoveFail{
		removeTransportErr: errors.New("cannot stop"),
	}
	prevEngine := engine
	engine = eng
	defer func() { engine = prevEngine }()

	oldTransports := []core.TransportConfig{
		{Name: "t1", Type: "mqtt"},
	}
	newTransports := []core.TransportConfig{
		{Name: "t1", Type: "http-push"}, // same name, different type → restart
	}
	err := diffTransports(oldTransports, newTransports, false, &diffActions{})
	if err == nil {
		t.Fatal("expected error from failed transport stop during restart, got nil")
	}
}

// mockEngineRollback records every engine operation in order and can
// fail an AddDriver/AddTransport/ApplyDerivedTags on demand, pinning
// the reload-rollback contract (audit CFG-2).
type mockEngineRollback struct {
	core.Engine
	failAddDriverName      string
	failAddTransportName   string
	failDerivedTags        bool
	ops                    []string
	rulesSetWith           [][]core.RuleConfig
	derivedTagsAppliedWith [][]core.DerivedTagConfig
}

func (m *mockEngineRollback) AddDriver(c core.DriverConfig) error {
	if c.Name == m.failAddDriverName {
		m.ops = append(m.ops, "add-driver:"+c.Name+":FAIL")
		return errors.New("injected add failure: " + c.Name)
	}
	m.ops = append(m.ops, "add-driver:"+c.Name)
	return nil
}
func (m *mockEngineRollback) RemoveDriver(name string) error {
	m.ops = append(m.ops, "remove-driver:"+name)
	return nil
}
func (m *mockEngineRollback) AddTransport(c core.TransportConfig) error {
	if c.Name == m.failAddTransportName {
		m.ops = append(m.ops, "add-transport:"+c.Name+":FAIL")
		return errors.New("injected add failure: " + c.Name)
	}
	m.ops = append(m.ops, "add-transport:"+c.Name)
	return nil
}
func (m *mockEngineRollback) RemoveTransport(name string) error {
	m.ops = append(m.ops, "remove-transport:"+name)
	return nil
}
func (m *mockEngineRollback) SetRules(rules []core.RuleConfig) error {
	m.ops = append(m.ops, "set-rules")
	m.rulesSetWith = append(m.rulesSetWith, rules)
	return nil
}
func (m *mockEngineRollback) ApplyDerivedTags(configs []core.DerivedTagConfig) error {
	if m.failDerivedTags {
		m.ops = append(m.ops, "apply-derived:FAIL")
		return errors.New("injected derived failure")
	}
	m.ops = append(m.ops, "apply-derived")
	m.derivedTagsAppliedWith = append(m.derivedTagsAppliedWith, configs)
	return nil
}
func (m *mockEngineRollback) Suspend() error { return nil }
func (m *mockEngineRollback) Resume() error  { return nil }

// TestApplyConfigRollsBackFailedDriverAdd verifies a failed driver diff
// restores the removed driver: the engine keeps running the pre-reload
// set and currentCfg still reflects it (audit CFG-2 — previously the
// removal stayed applied while /configs/raw showed the old config).
func TestApplyConfigRollsBackFailedDriverAdd(t *testing.T) {
	eng := &mockEngineRollback{failAddDriverName: "boom"}
	initial := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "modbus-tcp"},
			{Name: "d2", Type: "s7"},
		},
	}
	Init(eng, initial, "")
	defer Init(nil, nil, "")

	failed := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d2", Type: "s7"},
			{Name: "boom", Type: "modbus-tcp"},
		},
	}
	err := ApplyConfig(failed, false)
	if err == nil {
		t.Fatal("expected error from injected AddDriver failure")
	}

	// The diff removed d1, then failed to add boom, then the rollback
	// re-added d1 — the engine ends up on the pre-reload set.
	wantOps := []string{"remove-driver:d1", "add-driver:boom:FAIL", "add-driver:d1"}
	if !reflect.DeepEqual(eng.ops, wantOps) {
		t.Errorf("engine ops = %v, want %v", eng.ops, wantOps)
	}
	if cur := CurrentConfig(); cur == nil || len(cur.Drivers) != 2 || cur.Drivers[0].Name != "d1" {
		t.Errorf("currentCfg after failed reload = %+v, want the pre-reload config", cur)
	}
}

// TestApplyConfigRollsBackEverythingOnLaterFailure verifies a failure in
// a LATER diff step (transports) rolls back the already-applied driver
// changes, and a failure after the rule swap (derived tags) restores the
// previous rules too — the engine never ends up half-migrated.
func TestApplyConfigRollsBackEverythingOnLaterFailure(t *testing.T) {
	t.Run("transport failure rolls back driver swap", func(t *testing.T) {
		eng := &mockEngineRollback{failAddTransportName: "boom-t"}
		initial := &core.Config{
			Drivers: []core.DriverConfig{{Name: "d1", Type: "modbus-tcp"}},
		}
		Init(eng, initial, "")
		defer Init(nil, nil, "")

		failed := &core.Config{
			Drivers:    []core.DriverConfig{{Name: "d2", Type: "s7"}},
			Transports: []core.TransportConfig{{Name: "boom-t", Type: "mqtt"}},
			Rules:      []core.RuleConfig{{Name: "r2", Match: "ALL", Action: "forward", Target: "boom-t"}},
		}
		if err := ApplyConfig(failed, false); err == nil {
			t.Fatal("expected error from injected AddTransport failure")
		}
		// Driver swap applied, transport add failed, driver swap rolled back.
		wantOps := []string{
			"remove-driver:d1", "add-driver:d2",
			"add-transport:boom-t:FAIL",
			"remove-driver:d2", "add-driver:d1",
		}
		if !reflect.DeepEqual(eng.ops, wantOps) {
			t.Errorf("engine ops = %v, want %v", eng.ops, wantOps)
		}
		// Rules were never applied (the failure precedes the rule step):
		// no set-rules calls at all.
		if len(eng.rulesSetWith) != 0 {
			t.Errorf("rules were applied %d times, want 0 (rollback restores nothing unapplied)", len(eng.rulesSetWith))
		}
	})

	t.Run("derived failure restores applied rules and driver swap", func(t *testing.T) {
		eng := &mockEngineRollback{failDerivedTags: true}
		initial := &core.Config{
			Drivers:     []core.DriverConfig{{Name: "d1", Type: "modbus-tcp"}},
			DerivedTags: []core.DerivedTagConfig{{Name: "kpi1", Expr: "a * 2", Interval: "5s"}},
		}
		Init(eng, initial, "")
		defer Init(nil, nil, "")

		failed := &core.Config{
			Drivers:     []core.DriverConfig{{Name: "d2", Type: "s7"}},
			Rules:       []core.RuleConfig{{Name: "r2", Match: "ALL", Action: "drop"}},
			DerivedTags: []core.DerivedTagConfig{{Name: "kpi2", Expr: "b * 2", Interval: "5s"}},
		}
		if err := ApplyConfig(failed, false); err == nil {
			t.Fatal("expected error from injected derived-tags failure")
		}
		wantOps := []string{
			"remove-driver:d1", "add-driver:d2",
			"set-rules",
			"apply-derived:FAIL",
			"remove-driver:d2", "add-driver:d1",
			"set-rules", // rules restored to the pre-reload set
		}
		if !reflect.DeepEqual(eng.ops, wantOps) {
			t.Errorf("engine ops = %v, want %v", eng.ops, wantOps)
		}
		// The restoring SetRules call got the OLD rule set (empty), not
		// the failed reload's r2.
		last := eng.rulesSetWith[len(eng.rulesSetWith)-1]
		if len(last) != 0 {
			t.Errorf("rules restore got %+v, want the pre-reload (empty) set", last)
		}
	})
}
