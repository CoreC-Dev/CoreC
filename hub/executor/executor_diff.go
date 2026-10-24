package executor

import (
	"fmt"
	"log/slog"
	"reflect"

	"github.com/CoreC-Dev/CoreC/core"
)

// diffActions records everything a config diff has already applied to
// the running engine, so a later failure can roll the engine back to
// the pre-reload state. Without it a half-applied reload leaves the
// engine inconsistent with currentCfg: removed
// components stay removed while /configs/raw still shows them, with
// no self-healing and no way to see the real component set.
//
// Removals are recorded as the OLD config (not just the name) so the
// rollback can re-create the exact pre-reload component, and additions
// are recorded as names so the rollback can remove them. rulesApplied
// tracks the one-shot SetRules so a failure in a LATER diff step can
// restore the previous rule set too.
type diffActions struct {
	removedDrivers    []core.DriverConfig
	addedDrivers      []string
	removedTransports []core.TransportConfig
	addedTransports   []string
	rulesApplied      bool
}

// rollback best-effort undoes every recorded action in reverse order
// of application (transports were applied after drivers; rules last).
// Every failure is logged and the rollback continues — a partial
// restoration is strictly better than a half-applied reload.
func (a *diffActions) rollback(oldCfg *core.Config) {
	for _, name := range a.addedTransports {
		if err := engine.RemoveTransport(name); err != nil {
			slog.Error("config reload rollback: failed to remove transport added by failed reload",
				"name", name, "error", err)
		} else {
			slog.Warn("config reload failed: removed transport added by failed reload", "name", name)
		}
	}
	for i := len(a.removedTransports) - 1; i >= 0; i-- {
		cfg := a.removedTransports[i]
		if err := engine.AddTransport(cfg); err != nil {
			slog.Error("config reload rollback: failed to restore transport",
				"name", cfg.Name, "error", err)
		} else {
			slog.Warn("config reload failed: transport restored to pre-reload state", "name", cfg.Name)
		}
	}
	for _, name := range a.addedDrivers {
		if err := engine.RemoveDriver(name); err != nil {
			slog.Error("config reload rollback: failed to remove driver added by failed reload",
				"name", name, "error", err)
		} else {
			slog.Warn("config reload failed: removed driver added by failed reload", "name", name)
		}
	}
	for i := len(a.removedDrivers) - 1; i >= 0; i-- {
		cfg := a.removedDrivers[i]
		if err := engine.AddDriver(cfg); err != nil {
			slog.Error("config reload rollback: failed to restore driver",
				"name", cfg.Name, "error", err)
		} else {
			slog.Warn("config reload failed: driver restored to pre-reload state", "name", cfg.Name)
		}
	}
	if a.rulesApplied && oldCfg != nil {
		if err := engine.SetRules(oldCfg.Rules); err != nil {
			slog.Error("config reload rollback: failed to restore rules", "error", err)
		} else {
			slog.Warn("config reload failed: rules restored to pre-reload state", "count", len(oldCfg.Rules))
		}
	}
}

// diffDrivers reconciles the driver set: removes drivers that
// disappeared from the config, restarts modified ones, and adds new
// ones. Every mutation is recorded in act so ApplyConfig can roll the
// engine back to the pre-reload set if a later step fails.
func diffDrivers(oldDrivers, newDrivers []core.DriverConfig, force bool, act *diffActions) error {
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
			act.removedDrivers = append(act.removedDrivers, oldMap[name])
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
			act.addedDrivers = append(act.addedDrivers, name)
		} else if force || !reflect.DeepEqual(oldDriver, newDriver) {
			slog.Info("reloading modified driver", "name", name)
			if err := engine.RemoveDriver(name); err != nil {
				return fmt.Errorf("failed to stop driver %s for restart: %w", name, err)
			}
			act.removedDrivers = append(act.removedDrivers, oldDriver)
			if err := engine.AddDriver(newDriver); err != nil {
				return fmt.Errorf("failed to restart driver %s: %w", name, err)
			}
			act.addedDrivers = append(act.addedDrivers, name)
		}
	}

	return nil
}

// diffTransports reconciles the transport set, recording mutations in
// act for rollback (mirrors diffDrivers).
func diffTransports(oldTransports, newTransports []core.TransportConfig, force bool, act *diffActions) error {
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
			act.removedTransports = append(act.removedTransports, oldMap[name])
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
			act.addedTransports = append(act.addedTransports, name)
		} else if force || !reflect.DeepEqual(oldTransport, newTransport) {
			slog.Info("reloading modified transport", "name", name)
			if err := engine.RemoveTransport(name); err != nil {
				return fmt.Errorf("failed to stop transport %s for restart: %w", name, err)
			}
			act.removedTransports = append(act.removedTransports, oldTransport)
			if err := engine.AddTransport(newTransport); err != nil {
				return fmt.Errorf("failed to restart transport %s: %w", name, err)
			}
			act.addedTransports = append(act.addedTransports, name)
		}
	}

	return nil
}
