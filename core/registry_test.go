package core

import (
	"testing"
)

// --- Registry tests ---

func TestRegisterAndCreateDriver(t *testing.T) {
	t.Parallel()
	// Use a unique type name to avoid interfering with real registrations.
	typeName := "test-driver-registry"

	factory := func(config DriverConfig) (Driver, error) {
		return nil, nil
	}
	RegisterDriver(typeName, factory)

	// Should be in the registered list
	found := false
	for _, name := range RegisteredDrivers() {
		if name == typeName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("driver type %q not found in RegisteredDrivers()", typeName)
	}

	// CreateDriver should succeed
	_, err := CreateDriver(DriverConfig{Type: typeName})
	if err != nil {
		t.Fatalf("CreateDriver failed: %v", err)
	}
}

func TestCreateDriverUnknownType(t *testing.T) {
	t.Parallel()
	_, err := CreateDriver(DriverConfig{Type: "nonexistent-driver-type"})
	if err == nil {
		t.Fatal("expected error for unknown driver type, got nil")
	}
}

func TestRegisterAndCreateTransport(t *testing.T) {
	t.Parallel()
	typeName := "test-transport-registry"

	factory := func(config TransportConfig) (Transport, error) {
		return nil, nil
	}
	RegisterTransport(typeName, factory)

	found := false
	for _, name := range RegisteredTransports() {
		if name == typeName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("transport type %q not found in RegisteredTransports()", typeName)
	}

	_, err := CreateTransport(TransportConfig{Type: typeName})
	if err != nil {
		t.Fatalf("CreateTransport failed: %v", err)
	}
}

func TestCreateTransportUnknownType(t *testing.T) {
	t.Parallel()
	_, err := CreateTransport(TransportConfig{Type: "nonexistent-transport-type"})
	if err == nil {
		t.Fatal("expected error for unknown transport type, got nil")
	}
}

func TestRegisteredDriversReturnsCopy(t *testing.T) {
	t.Parallel()
	// RegisteredDrivers should return a snapshot; modifying it must not
	// affect the registry.
	names := RegisteredDrivers()
	if len(names) > 0 {
		original := names[0]
		names[0] = "tampered"
		again := RegisteredDrivers()
		if again[0] != original {
			t.Fatalf("RegisteredDrivers() returned a reference, not a copy")
		}
	}
}
