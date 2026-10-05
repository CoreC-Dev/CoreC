package executor

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

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

	route.ReloadFunc = Reload
	route.PatchFunc = Patch
	route.GetConfigFunc = CurrentConfig
	// Path A: expose the raw config (GET /configs/raw) and the dry-run
	// validator (POST /configs/validate) to the route layer without creating an
	// import cycle (executor → route, never the reverse).
	route.GetRawConfigFunc = RawConfigYAML
	route.ValidateFunc = Validate
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

	// Validate the parsed config.
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	// Surface non-blocking idle-mode warnings for the config being applied.
	// An empty/partial config is valid (dashboard-driven workflow); these
	// warnings let the operator see the core is running in idle mode.
	for _, w := range config.IdleWarnings(cfg) {
		slog.Warn(w)
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
		} else {
			return fmt.Errorf("invalid log-level %q", lvl)
		}
	}
	return nil
}
