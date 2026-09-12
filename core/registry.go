package core

import (
	"fmt"
	"sync"
)

// DriverFactory creates a Driver instance from configuration.
type DriverFactory func(config DriverConfig) (Driver, error)

// TransportFactory creates a Transport instance from configuration.
type TransportFactory func(config TransportConfig) (Transport, error)

var (
	globalRegistry = &Registry{
		drivers:    make(map[string]DriverFactory),
		transports: make(map[string]TransportFactory),
	}
)

// Registry holds driver and transport factories.
type Registry struct {
	mu         sync.RWMutex
	drivers    map[string]DriverFactory
	transports map[string]TransportFactory
}

// RegisterDriver registers a driver factory by type name.
// Typically called in a driver package's init() function.
func RegisterDriver(typeName string, factory DriverFactory) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.drivers[typeName] = factory
}

// RegisterTransport registers a transport factory by type name.
func RegisterTransport(typeName string, factory TransportFactory) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.transports[typeName] = factory
}

// CreateDriver creates a driver instance using the registered factory.
func CreateDriver(config DriverConfig) (Driver, error) {
	globalRegistry.mu.RLock()
	factory, ok := globalRegistry.drivers[config.Type]
	globalRegistry.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown driver type: %s", config.Type)
	}
	return factory(config)
}

// CreateTransport creates a transport instance using the registered factory.
func CreateTransport(config TransportConfig) (Transport, error) {
	globalRegistry.mu.RLock()
	factory, ok := globalRegistry.transports[config.Type]
	globalRegistry.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown transport type: %s", config.Type)
	}
	return factory(config)
}

// RegisteredDrivers returns all registered driver type names.
func RegisteredDrivers() []string {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()
	names := make([]string, 0, len(globalRegistry.drivers))
	for name := range globalRegistry.drivers {
		names = append(names, name)
	}
	return names
}

// RegisteredTransports returns all registered transport type names.
func RegisteredTransports() []string {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()
	names := make([]string, 0, len(globalRegistry.transports))
	for name := range globalRegistry.transports {
		names = append(names, name)
	}
	return names
}
