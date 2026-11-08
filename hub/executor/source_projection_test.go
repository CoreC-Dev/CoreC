package executor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CoreC-Dev/CoreC/config"
	"github.com/CoreC-Dev/CoreC/core"
)

// TestRawConfigYAMLProjectsTagsFileRoundTrip is the P0-1 regression: a
// tags-file driver must round-trip through GET /configs/raw and PUT
// /configs without re-expanding the file and colliding on tag names.
func TestRawConfigYAMLProjectsTagsFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tagsPath := filepath.Join(dir, "tags.yaml")
	if err := os.WriteFile(tagsPath, []byte(
		"- name: file_tag\n  address: \"40001\"\n  type: float32\n  interval: 1s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	yml := "drivers:\n" +
		"  - name: plc\n" +
		"    type: modbus-tcp\n" +
		"    tags-file: " + tagsPath + "\n" +
		"    tags:\n" +
		"      - name: inline_tag\n" +
		"        address: \"40002\"\n" +
		"        type: float32\n" +
		"        interval: 1s\n" +
		"    settings:\n" +
		"      host: 127.0.0.1\n" +
		"      port: 502\n"
	if err := os.WriteFile(cfgPath, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	Init(&mockEngineForExecutor{}, cfg, cfgPath)

	raw, err := RawConfigYAML()
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	reloaded, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("raw -> parse round trip rejected: %v\n%s", err, raw)
	}
	if got := len(reloaded.Drivers[0].Tags); got != 2 {
		t.Fatalf("reloaded tags = %d, want 2 (1 file + 1 inline)\n%s", got, raw)
	}
}

func TestCloneForMutationIsolatesSlices(t *testing.T) {
	cfg := &core.Config{
		Drivers:    []core.DriverConfig{{Name: "a"}, {Name: "b"}},
		Transports: []core.TransportConfig{{Name: "t"}},
		Rules:      []core.RuleConfig{{Name: "r"}},
	}
	clone := cloneForMutation(cfg)
	clone.Drivers[0].Name = "changed"
	clone.Drivers = append(clone.Drivers[:0], clone.Drivers[1:]...)

	if cfg.Drivers[0].Name != "a" || len(cfg.Drivers) != 2 || cfg.Drivers[1].Name != "b" {
		t.Fatalf("mutation leaked into the live config: %+v", cfg.Drivers)
	}
	if clone.Drivers[0].Name != "b" || len(clone.Drivers) != 1 {
		t.Fatalf("clone mutation did not apply to the clone: %+v", clone.Drivers)
	}
}

type ruleValidatorStub struct {
	mockEngineForExecutor
	err    error
	called bool
}

func (s *ruleValidatorStub) ValidateRules(*core.Config) error {
	s.called = true
	return s.err
}

func TestValidateAsksEngineToCompileRules(t *testing.T) {
	stub := &ruleValidatorStub{err: errors.New("invalid match expression: unexpected EOF")}
	Init(stub, &core.Config{}, "")

	_, err := Validate("global:\n  log-level: info\n")
	if err == nil || !stub.called {
		t.Fatalf("Validate must call the engine's rule compiler; called=%v err=%v", stub.called, err)
	}
}
