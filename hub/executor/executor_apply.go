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

	// Step 3: Diff and update drivers
	if err := diffDrivers(oldCfg.Drivers, cfg.Drivers, force); err != nil {
		slog.Warn("config reload partially applied: driver diff failed; "+
			"engine state may be inconsistent with currentCfg until next successful reload",
			"error", err)
		return fmt.Errorf("error updating drivers: %w", err)
	}

	// Step 4: Diff and update transports
	if err := diffTransports(oldCfg.Transports, cfg.Transports, force); err != nil {
		slog.Warn("config reload partially applied: transport diff failed; "+
			"engine state may be inconsistent with currentCfg until next successful reload",
			"error", err)
		return fmt.Errorf("error updating transports: %w", err)
	}

	// Step 5: Update rules
	if force || !reflect.DeepEqual(oldCfg.Rules, cfg.Rules) {
		if err := engine.SetRules(cfg.Rules); err != nil {
			return fmt.Errorf("error updating rules: %w", err)
		}
		slog.Info("rules updated", "count", len(cfg.Rules))
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
//     (problem 5).
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
}
