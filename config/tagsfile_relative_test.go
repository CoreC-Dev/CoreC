package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadResolvesTagsFileRelativeToConfig is the P2-4 regression: a
// relative tags-file is resolved against the config file's directory, not
// the process working directory.
func TestLoadResolvesTagsFileRelativeToConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tags.yaml"), []byte(
		"- name: t1\n  address: \"40001\"\n  type: float32\n  interval: 1s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	yml := "drivers:\n" +
		"  - name: plc\n" +
		"    type: modbus-tcp\n" +
		"    tags-file: tags.yaml\n" +
		"    settings:\n" +
		"      host: 127.0.0.1\n" +
		"      port: 502\n"
	if err := os.WriteFile(cfgPath, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load with relative tags-file: %v", err)
	}
	if got := len(cfg.Drivers[0].Tags); got != 1 {
		t.Fatalf("tags = %d, want 1", got)
	}
	want := filepath.Join(dir, "tags.yaml")
	if cfg.Drivers[0].TagsFileResolved != want {
		t.Errorf("resolved = %q, want %q", cfg.Drivers[0].TagsFileResolved, want)
	}
}
