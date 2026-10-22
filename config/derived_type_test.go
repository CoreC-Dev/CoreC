package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDerivedTagStringTypeLoads pins the documented derived-tags contract:
// `type: float32` (the string form shown in docs/config/derived-tags.md and
// config.example.yaml) must load. Before core.DataType gained UnmarshalYAML,
// copying those examples failed with "cannot unmarshal string into
// core.DataType" — the documented form was un-loadable.
func TestDerivedTagStringTypeLoads(t *testing.T) {
	yaml := []byte(`
global:
  api:
    listen: 0.0.0.0:9090
    secret: "test-secret-12345678"
drivers: []
derived-tags:
  - name: avg_temp
    expr: "(temp_01 + temp_02) / 2"
    type: float32
    interval: 2s
    on-missing: skip
    stale-guard: 30s
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("documented derived-tags form failed to load: %v", err)
	}
	if len(cfg.DerivedTags) != 1 {
		t.Fatalf("derived tags = %d, want 1", len(cfg.DerivedTags))
	}
	if cfg.DerivedTags[0].Type.String() != "float32" {
		t.Errorf("derived tag type = %q, want float32", cfg.DerivedTags[0].Type.String())
	}
}
