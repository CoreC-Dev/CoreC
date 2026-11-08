// Package driver_test verifies that every settings key the driver code
// parses is accepted by at least one ValidateSettings implementation of
// that driver family. Checking the production validators directly (rather
// than a second hand-maintained allowlist) catches both directions of
// drift: a new setting without a validator update, and a validator that
// rejects a setting the driver already accepts.
package driver_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/common/testutil"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/driver/modbus"
	"github.com/CoreC-Dev/CoreC/driver/opcua"
	"github.com/CoreC-Dev/CoreC/driver/s7"
)

// driverValidators lists the concrete variants of each driver family.
// A key is valid for the family when at least one variant accepts it,
// because variants intentionally expose different subsets (serial vs TCP,
// TLS cert files, subscription tuning).
var driverValidators = map[string][]core.SettingsValidator{
	"modbus": {
		&modbus.ModbusTCPDriver{},
		&modbus.ModbusRTUDriver{},
		&modbus.ModbusTLSDriver{},
		&modbus.ModbusNetDriver{},
	},
	"s7":    {&s7.S7Driver{}},
	"opcua": {&opcua.OPCUADriver{}},
}

func TestDriverSettingsAllowlistsCoverParsedKeys(t *testing.T) {
	// The driver packages live under driver/<name>.
	for pkg, validators := range driverValidators {
		t.Run(pkg, func(t *testing.T) {
			// The test binary's working directory IS driver/, so the
			// driver packages are direct subdirectories.
			parsed := scanPackageSettings(t, pkg)
			for key := range parsed {
				settings := map[string]any{key: ""}
				accepted := false
				for _, v := range validators {
					if err := v.ValidateSettings(settings); err == nil {
						accepted = true
						break
					}
				}
				if !accepted {
					t.Errorf("%s parses setting %q but no ValidateSettings implementation accepts it — update the driver's settings.go (otherwise configs using it are rejected)", pkg, key)
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
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		testutil.CollectParsedSettingKeys(string(data), out)
	}
	return out
}
