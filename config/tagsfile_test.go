package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/goccy/go-yaml"
)

func TestLoadTagsFiles(t *testing.T) {
	// Create a temporary tags file.
	dir := t.TempDir()
	tagsFile := filepath.Join(dir, "plc-tags.yaml")
	tagsContent := `
- name: temperature
  address: "40001"
  type: float32
  group: sensors
  interval: 1s
- name: pressure
  address: "40003"
  type: float32
  group: sensors
  interval: 1s
`
	if err := os.WriteFile(tagsFile, []byte(tagsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Config that references the tags file.
	yml := `
drivers:
  - name: plc
    type: modbus-tcp
    tags-file: ` + tagsFile + `
    settings:
      host: 192.168.1.100
      port: 502
transports:
  - name: mqtt
    type: mqtt
    settings:
      broker: tcp://localhost:1883
`

	cfg, err := Parse([]byte(yml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(cfg.Drivers) != 1 {
		t.Fatalf("expected 1 driver, got %d", len(cfg.Drivers))
	}
	dc := cfg.Drivers[0]
	if len(dc.Tags) != 2 {
		t.Fatalf("expected 2 tags from file, got %d", len(dc.Tags))
	}
	if dc.Tags[0].Name != "temperature" {
		t.Errorf("tag[0] name = %q, want %q", dc.Tags[0].Name, "temperature")
	}
	if dc.Tags[1].Name != "pressure" {
		t.Errorf("tag[1] name = %q, want %q", dc.Tags[1].Name, "pressure")
	}
}

func TestLoadTagsFilesWithInline(t *testing.T) {
	dir := t.TempDir()
	tagsFile := filepath.Join(dir, "plc-tags.yaml")
	tagsContent := `
- name: temperature
  address: "40001"
  type: float32
  interval: 1s
`
	if err := os.WriteFile(tagsFile, []byte(tagsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Config with both tags-file and inline tags.
	yml := `
drivers:
  - name: plc
    type: modbus-tcp
    tags-file: ` + tagsFile + `
    settings:
      host: 192.168.1.100
      port: 502
    tags:
      - name: inline_tag
        address: "40010"
        type: bool
        interval: 2s
transports:
  - name: mqtt
    type: mqtt
    settings:
      broker: tcp://localhost:1883
`

	cfg, err := Parse([]byte(yml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	dc := cfg.Drivers[0]
	if len(dc.Tags) != 2 {
		t.Fatalf("expected 2 tags (1 file + 1 inline), got %d", len(dc.Tags))
	}
	// File tags come first, inline tags appended.
	if dc.Tags[0].Name != "temperature" {
		t.Errorf("tag[0] name = %q, want %q (file tag)", dc.Tags[0].Name, "temperature")
	}
	if dc.Tags[1].Name != "inline_tag" {
		t.Errorf("tag[1] name = %q, want %q (inline tag)", dc.Tags[1].Name, "inline_tag")
	}
}

func TestLoadTagsFilesMissingFile(t *testing.T) {
	yml := `
drivers:
  - name: plc
    type: modbus-tcp
    tags-file: /nonexistent/path/tags.yaml
    settings:
      host: 192.168.1.100
      port: 502
transports:
  - name: mqtt
    type: mqtt
    settings:
      broker: tcp://localhost:1883
`

	_, err := Parse([]byte(yml))
	if err == nil {
		t.Fatal("expected error for missing tags file, got nil")
	}
}

func TestLoadTagsFilesNoFile(t *testing.T) {
	// Driver without tags-file should work normally.
	yml := `
drivers:
  - name: plc
    type: modbus-tcp
    settings:
      host: 192.168.1.100
      port: 502
    tags:
      - name: temperature
        address: "40001"
        type: float32
        interval: 1s
transports:
  - name: mqtt
    type: mqtt
    settings:
      broker: tcp://localhost:1883
`

	cfg, err := Parse([]byte(yml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(cfg.Drivers[0].Tags) != 1 {
		t.Fatalf("expected 1 inline tag, got %d", len(cfg.Drivers[0].Tags))
	}
}

// Ensure DriverConfig fields are correctly parsed.
func TestDriverConfigTagsFileFields(t *testing.T) {
	yml := `
drivers:
  - name: plc
    type: modbus-tcp
    tags-file: ./tags/plc.yaml
    tags-interval: 30s
    settings:
      host: 192.168.1.100
      port: 502
    tags:
      - name: temp
        address: "40001"
        type: float32
        interval: 1s
transports:
  - name: mqtt
    type: mqtt
    settings:
      broker: tcp://localhost:1883
`

	// We can't fully Parse because the tags-file doesn't exist,
	// but we can unmarshal to check the fields are parsed.
	cfg := &core.Config{}
	if err := yaml.Unmarshal([]byte(yml), cfg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	dc := cfg.Drivers[0]
	if dc.TagsFile != "./tags/plc.yaml" {
		t.Errorf("TagsFile = %q, want %q", dc.TagsFile, "./tags/plc.yaml")
	}
	if dc.TagsInterval != "30s" {
		t.Errorf("TagsInterval = %q, want %q", dc.TagsInterval, "30s")
	}
}
