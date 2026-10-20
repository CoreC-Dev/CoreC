package executor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/config"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/hub/route"
	"github.com/CoreC-Dev/CoreC/log"
	"github.com/goccy/go-yaml"
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

	route.ReloadFunc = ReloadWithOrigin
	route.PatchFunc = PatchWithOrigin
	route.GetConfigFunc = CurrentConfig
	// Path A: expose the raw config (GET /configs/raw) and the dry-run
	// validator (POST /configs/validate) to the route layer without creating an
	// import cycle (executor → route, never the reverse).
	route.GetRawConfigFunc = RawConfigYAML
	route.ValidateFunc = Validate
	// Server-side config backups (GET/POST /configs/backups,
	// GET /configs/backups/{name}, POST /configs/backups/{name}/restore):
	// same wiring pattern as above.
	route.ListConfigBackupsFunc = ListConfigBackups
	route.GetConfigBackupFunc = GetConfigBackup
	route.CreateConfigBackupFunc = CreateConfigBackup
	route.RestoreConfigBackupFunc = RestoreConfigBackup
	// Config change audit trail (audit finding HUB-4 / roadmap #22):
	// GET /configs/audit.
	route.ConfigAuditFunc = ConfigAuditHistory
	// Resource-level DELETE endpoints (audit HUB-3 / roadmap #26).
	route.RemoveDriverFunc = RemoveDriverByName
	route.RemoveTransportFunc = RemoveTransportByName
	route.RemoveRuleFunc = RemoveRuleByName
	route.ClassifyResourceError = classifyResourceError
}

// CurrentConfig returns the current active configuration.
func CurrentConfig() *core.Config {
	mux.Lock()
	defer mux.Unlock()
	return currentCfg
}

// ParseWithPath parses configuration from a file path WITHOUT validating.
// The executor's Reload path calls this so it can validate after parsing.
func ParseWithPath(path string) (*core.Config, error) {
	return config.LoadNoValidate(path)
}

// ParseWithBytes parses configuration from YAML bytes WITHOUT validating.
// See ParseWithPath for why validation is deferred.
func ParseWithBytes(buf []byte) (*core.Config, error) {
	return config.ParseNoValidate(buf)
}

// Validate performs a dry-run validation of a config payload WITHOUT applying
// it. It parses the payload, THEN validates — producing a faithful prediction
// of whether Reload would accept the config. Used by POST /configs/validate so
// the Dashboard can surface errors before the operator commits a PUT /configs.
func Validate(payload string) (warnings []string, err error) {
	if payload == "" {
		return nil, fmt.Errorf("empty config payload")
	}
	cfg, parseErr := ParseWithBytes([]byte(payload))
	if parseErr != nil {
		return nil, parseErr
	}
	if vErr := config.Validate(cfg); vErr != nil {
		return nil, fmt.Errorf("config validation failed: %w", vErr)
	}
	return config.IdleWarnings(cfg), nil
}

// RawConfigYAML returns the full active configuration as YAML text with all
// values in plaintext (no redaction). Used by GET /configs/raw so the
// Dashboard's Config Center can populate its editor with the server's real
// config, including secrets.
func RawConfigYAML() (string, error) {
	mux.Lock()
	cfg := currentCfg
	mux.Unlock()
	if cfg == nil {
		return "", fmt.Errorf("no active configuration")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal config: %w", err)
	}
	return string(data), nil
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
	return ReloadWithOrigin(path, payload, "")
}

// ReloadWithOrigin is Reload with the mutation origin recorded in the
// config audit trail (audit HUB-4): remote is the API caller's address.
func ReloadWithOrigin(path, payload, remote string) error {
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
		// Parse failures are client-fixable (the payload is broken
		// YAML): mark them so the route can answer 400 with the reason
		// instead of a masked 500 (audit CFG-3/HUB-2).
		return NewConfigError(fmt.Errorf("failed to parse config for reload: %w", err))
	}

	// Validate the parsed config.
	if err := config.Validate(cfg); err != nil {
		// Validation failures name the offending field/rule — safe
		// and actionable for the caller, unlike apply-stage errors
		// which may leak internal details and stay masked 500s.
		return NewConfigError(fmt.Errorf("config validation failed: %w", err))
	}

	// Surface non-blocking idle-mode warnings for the config being applied.
	// An empty/partial config is valid (dashboard-driven workflow); these
	// warnings let the operator see the core is running in idle mode.
	for _, w := range config.IdleWarnings(cfg) {
		slog.Warn(w)
	}

	if err := ApplyConfig(cfg, false); err != nil {
		return err
	}

	// Audit the accepted change (HUB-4): the apply succeeded, so the
	// engine now runs this config; the persist below is best-effort.
	recordConfigAudit(core.ConfigAuditEntry{
		At:     time.Now(),
		Action: "reload",
		Remote: remote,
		Keys:   reloadChangeKeys(path, payload),
	})

	// Persist the config to disk so changes survive a restart. When the reload
	// came from a payload (dashboard PUT /configs), write it back to the active
	// config file. Skip silently if no configPath is set (empty-config startup
	// with no file to write to).
	if payload != "" {
		mux.Lock()
		p := configPath
		mux.Unlock()
		if p != "" {
			// Auto-snapshot (server-side config backups): copy the current
			// on-disk config into the backup set immediately BEFORE it is
			// overwritten, so every successful dashboard-driven change has a
			// pre-change rollback point. Only runs when a file already
			// exists (first persist has nothing to back up) and is strictly
			// best-effort: a failed snapshot logs a warning and never blocks
			// the reload — availability of the change outranks the backup.
			snapshotExistingConfig(p)
			// 0o600: owner read/write only. The config file contains the API
			// secret and driver/transport credentials — world/group-readable
			// permissions would leak them to other users on the host.
			if err := os.WriteFile(p, []byte(payload), 0o600); err != nil {
				slog.Warn("config applied in memory but failed to persist to disk",
					"path", p, "error", err)
			} else {
				// os.WriteFile only applies perm on file creation; for an
				// existing file it truncates in place without changing mode.
				// Chmod explicitly so a previously world-readable config is
				// tightened to 0o600 and credentials are not leaked.
				if cerr := os.Chmod(p, 0o600); cerr != nil {
					slog.Warn("config persisted but failed to tighten file permissions",
						"path", p, "error", cerr)
				}
				slog.Info("config persisted to disk", "path", p)
			}
		}
	}

	return nil
}

// Patch updates selective runtime properties without full reload.
// supportedPatchKeys is the set of runtime settings that PATCH /configs
// can modify (roadmap #23: log-level, log-format, stale-threshold —
// the three knobs operators legitimately flip while debugging without
// deserving a full config reload).
var supportedPatchKeys = map[string]bool{
	"log-level":       true,
	"log-format":      true,
	"stale-threshold": true,
}

func Patch(patch map[string]any) error {
	return PatchWithOrigin(patch, "")
}

// PatchWithOrigin is Patch with the mutation origin recorded in the
// config audit trail (audit HUB-4).
func PatchWithOrigin(patch map[string]any, remote string) error {
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

	if fmtRaw, exists := patch["log-format"]; exists {
		format, ok := fmtRaw.(string)
		if !ok {
			return fmt.Errorf("log-format must be a string, got %T", fmtRaw)
		}
		switch format {
		case "", "text", "json":
			// "" keeps the current format (no-op), matching startup's
			// default of "text".
		default:
			return fmt.Errorf("invalid log-format %q (valid: text, json)", format)
		}
		if format != "" {
			// Re-Init swaps the inner stdout handler; the /logs event
			// bus chain (logCh → source observable) persists, so WS
			// subscribers are unaffected.
			log.Init(log.Level(), format)
			if currentCfg != nil {
				currentCfg.Global.LogFormat = format
			}
			slog.Info("log format patched", "format", format)
			recordConfigAudit(core.ConfigAuditEntry{
				At:     time.Now(),
				Action: "patch",
				Remote: remote,
				Keys:   []string{"log-format"},
			})
		}
	}
	if stRaw, exists := patch["stale-threshold"]; exists {
		stStr, ok := stRaw.(string)
		if !ok {
			return fmt.Errorf("stale-threshold must be a duration string, got %T", stRaw)
		}
		if stStr == "" {
			return fmt.Errorf("stale-threshold cannot be empty; use \"0\" to disable")
		}
		d, err := time.ParseDuration(stStr)
		if err != nil {
			return fmt.Errorf("invalid stale-threshold %q: %w", stStr, err)
		}
		if patcher, ok := engine.(core.StaleThresholdPatcher); ok && patcher != nil {
			patcher.SetStaleThreshold(d)
			if currentCfg != nil {
				currentCfg.Global.Engine.StaleThreshold = stStr
			}
			recordConfigAudit(core.ConfigAuditEntry{
				At:     time.Now(),
				Action: "patch",
				Remote: remote,
				Keys:   []string{"stale-threshold"},
			})
		} else {
			return fmt.Errorf("stale-threshold patching not supported by this engine")
		}
	}
	if lvlRaw, exists := patch["log-level"]; exists {
		lvl, ok := lvlRaw.(string)
		if !ok {
			return fmt.Errorf("log-level must be a string, got %T", lvlRaw)
		}
		// Empty string is a no-op (client sent an empty value, don't change).
		if lvl != "" {
			if l, found := log.ParseLevel(lvl); found {
				log.SetLevel(l)
				// Sync the in-memory active config so GET /configs/raw reflects the
				// runtime-patched level. Without this, the Config Center editor
				// would show the stale pre-PATCH value, and a subsequent Hot Reload
				// (PUT /configs) that did not touch log-level would silently
				// overwrite the patched level with the old value. currentCfg is the
				// source of truth for the running state (ApplyConfig sets it on
				// every successful reload); PATCH is a runtime state change, so it
				// must update currentCfg too. The on-disk config is untouched - a
				// CoreC restart still returns to the persisted level.
				if currentCfg != nil {
					currentCfg.Global.LogLevel = lvl
				}
				slog.Info("log level patched", "level", lvl)
				recordConfigAudit(core.ConfigAuditEntry{
					At:     time.Now(),
					Action: "patch",
					Remote: remote,
					Keys:   []string{"log-level"},
				})
			} else {
				return fmt.Errorf("invalid log-level %q", lvl)
			}
		}
	}
	return nil
}

// reloadChangeKeys summarizes what a reload touched for the audit trail:
// "payload" (dashboard/editor PUT) or the config path it loaded from.
func reloadChangeKeys(path, payload string) []string {
	if payload != "" {
		return []string{"payload"}
	}
	if path != "" {
		return []string{"path:" + path}
	}
	return []string{"active-file"}
}

// --- Resource-level sugar endpoints (audit HUB-3 / roadmap #26) ---
//
// Removing a single driver/transport/rule previously required the caller
// to fetch the full YAML, splice it, and PUT the whole config. These
// helpers do exactly that internally — mutate the active config, then
// run the SAME validate → apply → persist chain as PUT /configs
// (including rollback on failure, the pre-change backup snapshot, and
// the audit entry) — so the narrow API gets every safety rail for free.

// removeFromConfig removes one named entry from the given slice and
// reports whether it existed. Generics keep it copy-free at the call
// sites (the slices are small; simplicity wins).
func removeNamedDriver(cfg *core.Config, name string) bool {
	for i, d := range cfg.Drivers {
		if d.Name == name {
			cfg.Drivers = append(cfg.Drivers[:i], cfg.Drivers[i+1:]...)
			return true
		}
	}
	return false
}

func removeNamedTransport(cfg *core.Config, name string) bool {
	for i, t := range cfg.Transports {
		if t.Name == name {
			cfg.Transports = append(cfg.Transports[:i], cfg.Transports[i+1:]...)
			return true
		}
	}
	return false
}

func removeNamedRule(cfg *core.Config, name string) bool {
	for i, r := range cfg.Rules {
		if r.Name == name {
			cfg.Rules = append(cfg.Rules[:i], cfg.Rules[i+1:]...)
			return true
		}
	}
	return false
}

// removeResource is the shared body: snapshot the active config, drop
// the named entry, and reload through the full chain. Returns
// os.ErrNotExist-style error when the name is missing (the route maps
// it to 404).
func removeResource(kind, name, remote string, drop func(*core.Config) bool) error {
	mux.Lock()
	var snapshot *core.Config
	if currentCfg != nil {
		clone := *currentCfg
		snapshot = &clone
	}
	mux.Unlock()
	if snapshot == nil {
		return fmt.Errorf("no active configuration")
	}
	if !drop(snapshot) {
		return ErrResourceNotFound
	}
	payload, err := yaml.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("failed to re-marshal config: %w", err)
	}
	if err := ReloadWithOrigin("", string(payload), remote); err != nil {
		return err
	}
	recordConfigAudit(core.ConfigAuditEntry{
		At:      time.Now(),
		Action:  "reload",
		Remote:  remote,
		Keys:    []string{"delete-" + kind + ":" + name},
		Details: kind + " " + name + " removed via resource endpoint",
	})
	return nil
}

// ErrResourceNotFound reports a DELETE on a name the active config does
// not carry (stale client state).
var ErrResourceNotFound = fmt.Errorf("resource not found")

// RemoveDriverByName removes one driver through the full reload chain.
func RemoveDriverByName(name, remote string) error {
	return removeResource("driver", name, remote, func(cfg *core.Config) bool {
		return removeNamedDriver(cfg, name)
	})
}

// RemoveTransportByName removes one transport through the full reload chain.
func RemoveTransportByName(name, remote string) error {
	return removeResource("transport", name, remote, func(cfg *core.Config) bool {
		return removeNamedTransport(cfg, name)
	})
}

// RemoveRuleByName removes one rule through the full reload chain.
func RemoveRuleByName(name, remote string) error {
	return removeResource("rule", name, remote, func(cfg *core.Config) bool {
		return removeNamedRule(cfg, name)
	})
}
