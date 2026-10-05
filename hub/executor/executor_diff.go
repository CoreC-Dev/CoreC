package executor

import (
	"fmt"
	"log/slog"
	"reflect"

	"github.com/CoreC-Dev/CoreC/core"
)

func diffDrivers(oldDrivers, newDrivers []core.DriverConfig, force bool) error {
	oldMap := make(map[string]core.DriverConfig, len(oldDrivers))
	for _, d := range oldDrivers {
		oldMap[d.Name] = d
	}

	newMap := make(map[string]core.DriverConfig, len(newDrivers))
	for _, d := range newDrivers {
		newMap[d.Name] = d
	}

	// Remove deleted drivers
	for name := range oldMap {
		if _, exists := newMap[name]; !exists {
			slog.Info("removing driver", "name", name)
			if err := engine.RemoveDriver(name); err != nil {
				return fmt.Errorf("failed to remove driver %s: %w", name, err)
			}
		}
	}

	// Add new or update modified drivers
	for name, newDriver := range newMap {
		oldDriver, exists := oldMap[name]
		if !exists {
			slog.Info("adding new driver", "name", name, "type", newDriver.Type)
			if err := engine.AddDriver(newDriver); err != nil {
				return fmt.Errorf("failed to add driver %s: %w", name, err)
			}
		} else if force || !reflect.DeepEqual(oldDriver, newDriver) {
			slog.Info("reloading modified driver", "name", name)
			if err := engine.RemoveDriver(name); err != nil {
				return fmt.Errorf("failed to stop driver %s for restart: %w", name, err)
			}
			if err := engine.AddDriver(newDriver); err != nil {
				return fmt.Errorf("failed to restart driver %s: %w", name, err)
			}
		}
	}

	return nil
}

func diffTransports(oldTransports, newTransports []core.TransportConfig, force bool) error {
	oldMap := make(map[string]core.TransportConfig, len(oldTransports))
	for _, t := range oldTransports {
		oldMap[t.Name] = t
	}

	newMap := make(map[string]core.TransportConfig, len(newTransports))
	for _, t := range newTransports {
		newMap[t.Name] = t
	}

	// Remove deleted transports
	for name := range oldMap {
		if _, exists := newMap[name]; !exists {
			slog.Info("removing transport", "name", name)
			if err := engine.RemoveTransport(name); err != nil {
				return fmt.Errorf("failed to remove transport %s: %w", name, err)
			}
		}
	}

	// Add new or update modified transports
	for name, newTransport := range newMap {
		oldTransport, exists := oldMap[name]
		if !exists {
			slog.Info("adding new transport", "name", name, "type", newTransport.Type)
			if err := engine.AddTransport(newTransport); err != nil {
				return fmt.Errorf("failed to add transport %s: %w", name, err)
			}
		} else if force || !reflect.DeepEqual(oldTransport, newTransport) {
			slog.Info("reloading modified transport", "name", name)
			if err := engine.RemoveTransport(name); err != nil {
				return fmt.Errorf("failed to stop transport %s for restart: %w", name, err)
			}
			if err := engine.AddTransport(newTransport); err != nil {
				return fmt.Errorf("failed to restart transport %s: %w", name, err)
			}
		}
	}

	return nil
}
