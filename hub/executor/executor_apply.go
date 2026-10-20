package executor

import (
	"fmt"
	"log/slog"
	"reflect"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
)

// ApplyConfig applies configuration diffs to the running engine.
func ApplyConfig(cfg *core.Config, force bool) error {
	mux.Lock()
	defer mux.Unlock()

	if engine == nil {
		return fmt.Errorf("engine not initialized in executor")
	}

	slog.Info("applying configuration updates")

	// Step 1: Update log level
	if cfg.Global.LogLevel != "" {
		if lvl, ok := log.ParseLevel(cfg.Global.LogLevel); ok {
			log.SetLevel(lvl)
		}
	}

	// Step 1b/1c: Warn about config sections that cannot be hot-applied
	// (require a restart). Extracted to keep ApplyConfig's cyclomatic
	// complexity under the gocyclo threshold (CPLX-017).
	warnRestartRequired(currentCfg, cfg)

	// Step 2: Suspend engine to pause task ticks
	if err := engine.Suspend(); err != nil {
		slog.Error("failed to suspend engine during config reload", "error", err)
	}
	defer func() {
		// Ensure engine resumes even if diff application encounters partial errors
		if err := engine.Resume(); err != nil {
			slog.Error("failed to resume engine after config reload", "error", err)
		}
	}()

	oldCfg := currentCfg
	if oldCfg == nil {
		oldCfg = &core.Config{}
	}

	// act records every mutation the diffs apply so any later failure
	// can roll the engine back to the pre-reload component set (audit
	// finding CFG-2: a half-applied reload previously left the engine
	// inconsistent with currentCfg — removed components stayed removed
	// while /configs/raw kept showing them).
	act := &diffActions{}

	// Step 3: Diff and update drivers
	if err := diffDrivers(oldCfg.Drivers, cfg.Drivers, force, act); err != nil {
		act.rollback(oldCfg)
		slog.Warn("config reload rolled back to pre-reload state: driver diff failed",
			"error", err)
		return fmt.Errorf("error updating drivers: %w", err)
	}

	// Step 4: Diff and update transports
	if err := diffTransports(oldCfg.Transports, cfg.Transports, force, act); err != nil {
		act.rollback(oldCfg)
		slog.Warn("config reload rolled back to pre-reload state: transport diff failed",
			"error", err)
		return fmt.Errorf("error updating transports: %w", err)
	}

	// Step 5: Update rules
	if force || !reflect.DeepEqual(oldCfg.Rules, cfg.Rules) {
		if err := engine.SetRules(cfg.Rules); err != nil {
			act.rollback(oldCfg)
			slog.Warn("config reload rolled back to pre-reload state: rule update failed",
				"error", err)
			return fmt.Errorf("error updating rules: %w", err)
		}
		act.rulesApplied = true
		slog.Info("rules updated", "count", len(cfg.Rules))
	}

	// Step 5b: Hot-apply derived tags (audit finding CFG-1: changes to
	// derived-tags were previously ignored by ApplyConfig — PUT
	// returned success and nothing happened). The engine compiles the
	// new set before stopping the old evaluator, so a broken expression
	// keeps the previous tags running. Engines without the role keep
	// the restart-required warning path below.
	if force || !reflect.DeepEqual(oldCfg.DerivedTags, cfg.DerivedTags) {
		if applier, ok := engine.(core.DerivedTagApplier); ok && applier != nil {
			if err := applier.ApplyDerivedTags(cfg.DerivedTags); err != nil {
				// The evaluator itself is fail-safe (old set still
				// running), but drivers/transports/rules may already
				// have been swapped — roll them back so the engine
				// matches the pre-reload config exactly.
				act.rollback(oldCfg)
				slog.Warn("config reload rolled back to pre-reload state: derived tags update failed",
					"error", err)
				return fmt.Errorf("error updating derived tags: %w", err)
			}
			slog.Info("derived tags updated", "count", len(cfg.DerivedTags))
		} else {
			slog.Warn("derived-tags changed but this engine does not support hot-apply; requires restart to take effect")
		}
	}

	currentCfg = cfg
	slog.Info("configuration reload completed successfully")
	return nil
}

// warnRestartRequired logs warnings for configuration sections that
// ApplyConfig cannot hot-apply and therefore require a process restart to
// take effect:
//   - api.listen / api.secret / api TLS — the HTTP server is bound once at
//     boot; reload cannot rebind or re-authenticate it (m8),
//   - rule-providers, rule-groups, and engine tuning parameters — silently
//     ignored by ApplyConfig; warn so operators know a restart is needed
//     (problem 5),
//   - node (topology auto-discovery) and global.buffer (offline buffer) —
//     wired once at engine start; warn so operators know a restart is
//     needed (audit finding CFG-1: these were previously ignored with
//     no warning at all).
//
// Extracted from ApplyConfig to reduce its cyclomatic complexity (CPLX-017).
func warnRestartRequired(oldCfg, cfg *core.Config) {
	if oldCfg == nil {
		return
	}
	if cfg.Global.API.Listen != "" && cfg.Global.API.Listen != oldCfg.Global.API.Listen {
		slog.Warn("api.listen changed but requires restart to take effect",
			"old", oldCfg.Global.API.Listen, "new", cfg.Global.API.Listen)
	}
	if cfg.Global.API.Secret != "" && cfg.Global.API.Secret != oldCfg.Global.API.Secret {
		slog.Warn("api.secret changed but requires restart to take effect")
	}
	if cfg.Global.API.TLSCert != oldCfg.Global.API.TLSCert || cfg.Global.API.TLSKey != oldCfg.Global.API.TLSKey {
		slog.Warn("api TLS config changed but requires restart to take effect")
	}
	if !reflect.DeepEqual(oldCfg.RuleProviders, cfg.RuleProviders) {
		slog.Warn("rule-providers changed but requires restart to take effect")
	}
	if !reflect.DeepEqual(oldCfg.RuleGroups, cfg.RuleGroups) {
		slog.Warn("rule-groups changed but requires restart to take effect")
	}
	if !reflect.DeepEqual(oldCfg.Global.Engine, cfg.Global.Engine) {
		slog.Warn("engine tuning parameters changed but require restart to take effect")
	}
	if !reflect.DeepEqual(oldCfg.Node, cfg.Node) {
		slog.Warn("node (auto-discovery) config changed but requires restart to take effect")
	}
	if !reflect.DeepEqual(oldCfg.Global.Buffer, cfg.Global.Buffer) {
		slog.Warn("global.buffer (offline buffer) config changed but requires restart to take effect")
	}
}
