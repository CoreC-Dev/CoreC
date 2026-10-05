package executor

import (
	"errors"
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
	err := diffDrivers(oldDrivers, nil, false)
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
	err := diffDrivers(oldDrivers, nil, false)
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
	err := diffDrivers(oldDrivers, newDrivers, false)
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
	err := diffTransports(oldTransports, nil, false)
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
	err := diffTransports(oldTransports, nil, false)
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
	err := diffTransports(oldTransports, newTransports, false)
	if err == nil {
		t.Fatal("expected error from failed transport stop during restart, got nil")
	}
}
