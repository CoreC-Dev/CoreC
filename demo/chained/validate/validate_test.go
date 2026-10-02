package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/config"

	_ "github.com/CoreC-Dev/CoreC/driver/all"
	_ "github.com/CoreC-Dev/CoreC/transport/all"
)

// TEST-009: smoke test that all demo scenario configs and the example
// config parse and validate successfully. The demo configs use
// ${COREC_API_SECRET} for the API secret, so we set it before loading.

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root (go.mod)")
		}
		dir = parent
	}
}

func TestDemoConfigsValid(t *testing.T) {
	root := findModuleRoot(t)

	// Provide the env var that demo configs reference.
	os.Setenv("COREC_API_SECRET", "demo-token")
	defer os.Unsetenv("COREC_API_SECRET")

	pattern := filepath.Join(root, "demo", "chained", "scenario*", "*.yaml")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no demo config files found")
	}

	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		if _, err := config.Load(f); err != nil {
			t.Errorf("demo config %s failed validation: %v", rel, err)
		}
	}
}

func TestExampleConfigValid(t *testing.T) {
	root := findModuleRoot(t)
	example := filepath.Join(root, "config.example.yaml")
	if _, err := config.Load(example); err != nil {
		t.Errorf("config.example.yaml failed validation: %v", err)
	}
}

func TestNoHardcodedDemoTokenInConfigs(t *testing.T) {
	// SEC-005 regression: no demo YAML should contain the literal
	// "demo-token" as a secret value.
	root := findModuleRoot(t)
	pattern := filepath.Join(root, "demo", "chained", "scenario*", "*.yaml")
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		if strings.Contains(string(data), `secret: "demo-token"`) {
			t.Errorf("SEC-005 regression: %s still contains hardcoded secret \"demo-token\"", f)
		}
	}
}
