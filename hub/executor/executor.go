package executor

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/CoreC-Dev/CoreC/config"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/hub/route"
	"github.com/CoreC-Dev/CoreC/log"
)

var (
	mux        sync.Mutex
	engine     core.Engine
	currentCfg *core.Config
	configPath string
)

// Init initializes the executor with the active engine, config, and file path.
func Init(e core.Engine, initialCfg *core.Config, path string) {
	mux.Lock()
	engine = e
	currentCfg = initialCfg
	configPath = path
	mux.Unlock()

	route.ReloadFunc = Reload
	route.PatchFunc = Patch
	route.GetConfigFunc = CurrentConfig
}

// CurrentConfig returns the current active configuration.
func CurrentConfig() *core.Config {
	mux.Lock()
	defer mux.Unlock()
	return currentCfg
}

// ParseWithPath parses configuration from a file path.
func ParseWithPath(path string) (*core.Config, error) {
	return config.Load(path)
}

// ParseWithBytes parses configuration from YAML bytes.
func ParseWithBytes(buf []byte) (*core.Config, error) {
	return config.Parse(buf)
}

// resolveConfigPath validates and resolves a client-supplied config file path
// to prevent path traversal attacks. The requested path must resolve to a
// file inside the directory of the currently persisted config path. This
// prevents authenticated callers from reading arbitrary files such as
// ../../etc/passwd via the PUT /configs endpoint.
func resolveConfigPath(requested string) (string, error) {
	mux.Lock()
	base := configPath
	mux.Unlock()

	if base == "" {
		return "", fmt.Errorf("no base config path is set; cannot resolve relative path")
	}

	baseDir := filepath.Dir(filepath.Clean(base))
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base config directory: %w", err)
	}

	// Reject absolute paths — only allow paths relative to the config directory.
	if filepath.IsAbs(requested) {
		return "", fmt.Errorf("absolute config paths are not allowed")
	}

	cleaned := filepath.Clean(requested)
	// Reject any component that escapes the base directory.
	if strings.HasPrefix(cleaned, "..") || cleaned == ".." || strings.HasPrefix(cleaned, string(filepath.Separator)) {
		return "", fmt.Errorf("config path escapes the allowed directory: %s", requested)
	}

	full := filepath.Join(absBaseDir, cleaned)
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("failed to resolve config path: %w", err)
	}

	// Final containment check: the resolved absolute path must be within absBaseDir.
	rel, err := filepath.Rel(absBaseDir, absFull)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("config path escapes the allowed directory: %s", requested)
	}

	return absFull, nil
}

// Reload executes a full configuration reload either from a file or payload string.
func Reload(path, payload string) error {
	var (
		cfg *core.Config
		err error
	)

	if payload != "" {
		cfg, err = ParseWithBytes([]byte(payload))
	} else {
		targetPath := path
		if targetPath == "" {
			mux.Lock()
			targetPath = configPath
			mux.Unlock()
		} else {
			// Validate the client-supplied path to prevent traversal attacks.
			targetPath, err = resolveConfigPath(path)
			if err != nil {
				return fmt.Errorf("invalid config path: %w", err)
			}
		}
		if targetPath == "" {
			return fmt.Errorf("no config path specified")
		}
		cfg, err = ParseWithPath(targetPath)
		if err == nil && path != "" {
			mux.Lock()
			configPath = targetPath
			mux.Unlock()
		}
	}

	if err != nil {
		return fmt.Errorf("failed to parse config for reload: %w", err)
	}

	return ApplyConfig(cfg, false)
}

// Patch updates selective runtime properties without full reload.
// supportedPatchKeys is the set of runtime settings that PATCH /configs can modify.
var supportedPatchKeys = map[string]bool{
	"log-level": true,
}

func Patch(patch map[string]any) error {
	mux.Lock()
	defer mux.Unlock()

	// Reject unknown keys instead of silently ignoring them.
	var unknown []string
	for k := range patch {
		if !supportedPatchKeys[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unsupported patch key(s): %v (supported: log-level)", unknown)
	}

	if lvl, ok := patch["log-level"].(string); ok && lvl != "" {
		if l, found := log.ParseLevel(lvl); found {
			log.SetLevel(l)
			slog.Info("log level patched", "level", lvl)
		} else {
			return fmt.Errorf("invalid log-level %q", lvl)
		}
	}
	return nil
}

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

	// Step 1b: Warn if API config changes can't be applied without restart (m8).
	// The HTTP server is started once at boot; reload cannot rebind or re-authenticate it.
	if oldCfg := currentCfg; oldCfg != nil {
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

		// Step 1c: Warn about config sections that ApplyConfig does not
		// hot-apply. Rule providers, rule groups, and engine tuning
		// parameters are silently ignored — warn so operators know a
		// restart is needed (problem 5).
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
				slog.Error("failed to remove driver", "name", name, "error", err)
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
				slog.Error("failed to stop driver for restart", "name", name, "error", err)
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
				slog.Error("failed to remove transport", "name", name, "error", err)
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
				slog.Error("failed to stop transport for restart", "name", name, "error", err)
			}
			if err := engine.AddTransport(newTransport); err != nil {
				return fmt.Errorf("failed to restart transport %s: %w", name, err)
			}
		}
	}

	return nil
}
