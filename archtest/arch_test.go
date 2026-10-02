// Package archtest contains architecture structure tests that mechanically enforce
// the dependency direction rules defined in ARCHITECTURE.md.
//
// These tests run `go list -json ./...` to obtain the actual import graph,
// then verify every package's imports comply with the allowed-edge table.
// On violation, the error message includes a fix instruction.
package archtest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/CoreC-Dev/CoreC"

// findModuleRoot walks up from cwd to find the directory containing go.mod.
func findModuleRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("go.mod not found")
		}
		dir = parent
	}
}

// goListPkg represents the relevant fields of `go list -json` output.
type goListPkg struct {
	ImportPath string   `json:"ImportPath"`
	Deps       []string `json:"Deps"`
}

// allowedDeps returns the set of allowed internal dependency prefixes for a package.
// Returns nil if the package may depend on anything (wiring points).
// Returns an empty (non-nil) slice if the package may not depend on any internal package.
func allowedDeps(pkg string) []string {
	// Wiring points: may import anything
	switch pkg {
	case "cmd/corec", "driver/all", "transport/all":
		return nil // unrestricted
	}
	if strings.HasPrefix(pkg, "demo/") {
		return nil // demo packages are wiring points
	}

	// Define allowed dependency prefixes per package
	switch {
	case pkg == "core":
		return []string{} // pure interfaces/types, no internal deps

	case pkg == "common/metrics" || pkg == "common/observable" ||
		pkg == "common/trace" || pkg == "common/util":
		return []string{} // pure utils, no internal deps

	case pkg == "log":
		return []string{"core", "common/observable"}

	case pkg == "config":
		return []string{"core", "common/"}

	case pkg == "driverbase":
		return []string{"core", "common/"}

	case strings.HasPrefix(pkg, "driver/") && pkg != "driver/all":
		return []string{"core", "common/", "driverbase"}

	case strings.HasPrefix(pkg, "transport/") && pkg != "transport/all":
		return []string{"core", "common/", "transport/parser"}

	case pkg == "rule":
		return []string{"core", "common/"}

	case pkg == "engine" || strings.HasPrefix(pkg, "engine/"):
		return []string{"core", "common/", "rule", "log", "engine/"}

	case strings.HasPrefix(pkg, "hub/"):
		return []string{"core", "common/", "config", "log", "hub/"}

	case pkg == "hub":
		return []string{"core", "common/", "config", "log", "hub/"}

	default:
		return nil // unknown package, allow (don't block)
	}
}

// crossDomainForbidden returns true if pkg importing dep is a forbidden cross-domain edge.
func crossDomainForbidden(pkg, dep string) bool {
	// driver/* cannot depend on other driver/* (except via driver/all)
	if strings.HasPrefix(pkg, "driver/") && pkg != "driver/all" &&
		strings.HasPrefix(dep, "driver/") && dep != "driver/all" && dep != pkg {
		return true
	}
	// transport/mqtt cannot depend on transport/httppush (and vice versa)
	if pkg == "transport/mqtt" && dep == "transport/httppush" {
		return true
	}
	if pkg == "transport/httppush" && dep == "transport/mqtt" {
		return true
	}
	return false
}

func TestDependencyDirection(t *testing.T) {
	// Run `go list -json ./...` from the module root to get the full import graph
	root := findModuleRoot()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, output)
	}

	// Parse concatenated JSON objects
	dec := json.NewDecoder(strings.NewReader(string(output)))
	var pkgs []goListPkg
	for dec.More() {
		var p goListPkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("failed to decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}

	violations := 0
	for _, p := range pkgs {
		pkg := strings.TrimPrefix(p.ImportPath, modulePath+"/")
		if pkg == p.ImportPath {
			continue // not in this module
		}
		allowed := allowedDeps(pkg)
		if allowed == nil {
			continue // unrestricted (wiring point)
		}

		for _, dep := range p.Deps {
			// Skip stdlib and third-party deps
			if !strings.HasPrefix(dep, modulePath+"/") {
				continue
			}
			depShort := strings.TrimPrefix(dep, modulePath+"/")

			// Check cross-domain prohibition
			if crossDomainForbidden(pkg, depShort) {
				t.Errorf(
					"ARCH VIOLATION: %s imports %s (cross-domain forbidden)\n"+
						"  Fix: %s must not directly depend on %s. Use the core interface or the registry instead.",
					pkg, depShort, pkg, depShort)
				violations++
				continue
			}

			// Check against allowed prefixes
			ok := false
			for _, prefix := range allowed {
				if depShort == prefix || strings.HasPrefix(depShort, prefix) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf(
					"ARCH VIOLATION: %s imports %s (not in allowed dependencies)\n"+
						"  Allowed: %v\n"+
						"  Fix: Remove the import of %s from %s, or move the shared code to a lower layer (core/common).",
					pkg, depShort, allowed, depShort, pkg)
				violations++
			}
		}
	}

	if violations > 0 {
		t.Fatalf("\n%d dependency direction violations found.\nSee ARCHITECTURE.md for the allowed-edge table.", violations)
	}
}
