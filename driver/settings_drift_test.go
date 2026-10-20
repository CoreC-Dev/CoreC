// Package driver_test hosts cross-package settings-allowlist drift tests
// (audit CFG-4 / roadmap #25): each ValidateSettings implementation
// declares its known keys; this test scans the production sources for
// every settings key the code actually parses and fails when a parsed
// key is missing from the declared set — so adding a setting without
// updating the validator is caught at CI time, not by a user's config.
package driver_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	driver "github.com/CoreC-Dev/CoreC/driver"
)

// knownDriverSettings maps driver type → allowlisted settings keys, per
// the ValidateSettings implementations in each driver package.
var knownDriverSettings = map[string]map[string]bool{
	"modbus": {
		"slave-id": true, "retry": true, "timeout": true,
		"reconnect-interval": true, "reconnect-max-interval": true, "max-reconnect-failures": true,
		"host": true, "port": true, "serial-device": true, "baud-rate": true,
		"data-bits": true, "parity": true, "stop-bits": true,
		"cert-file": true, "key-file": true, "ca-file": true,
	},
	"s7": {
		"host": true, "port": true, "rack": true, "slot": true,
		"timeout": true, "idle-timeout": true,
		"reconnect-interval": true, "reconnect-max-interval": true, "max-reconnect-failures": true,
	},
	"opcua": {
		"endpoint": true, "security-policy": true, "security-mode": true,
		"username": true, "password": true, "timeout": true,
		"cert-file": true, "key-file": true,
		"subscription-interval": true, "subscription-buffer": true,
		"max-batch-size": true, "mode": true,
		"reconnect-interval": true, "reconnect-max-interval": true, "max-reconnect-failures": true,
	},
}

func TestDriverSettingsAllowlistsCoverParsedKeys(t *testing.T) {
	// The driver packages live under driver/<name>.
	for pkg, known := range knownDriverSettings {
		t.Run(pkg, func(t *testing.T) {
			// The test binary's working directory IS driver/, so the
			// driver packages are direct subdirectories.
			parsed := scanPackageSettings(t, pkg)
			for key := range parsed {
				if !known[key] {
					t.Errorf("%s parses setting %q but its ValidateSettings allowlist does not include it — update the driver's settings.go (otherwise configs using it are rejected)", pkg, key)
				}
			}
		})
	}
}

// scanPackageSettings walks the package's non-test sources and collects
// every settings key referenced by either parsing idiom.
func scanPackageSettings(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// The allowlist declaration itself would false-positive the
		// scan (it mentions keys as literals, but not in the two
		// parsing idioms — the regexes only match calls/subscripts, so
		// settings.go is actually safe to scan; no skip needed).
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		driver.CollectParsedSettingKeys(string(data), out)
	}
	return out
}
