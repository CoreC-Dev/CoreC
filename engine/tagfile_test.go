package engine

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestTagFileWatcherDetectsChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tags.yaml")

	// Initial content: 2 tags.
	initial := `
- name: temperature
  address: "40001"
  type: float32
  interval: 1s
- name: pressure
  address: "40003"
  type: float32
  interval: 1s
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	var reloadCount atomic.Int32
	var lastTagsMu sync.Mutex
	var lastTags []core.TagConfig

	onReload := func(tags []core.TagConfig) error {
		reloadCount.Add(1)
		lastTagsMu.Lock()
		lastTags = tags
		lastTagsMu.Unlock()
		return nil
	}

	w, err := newTagFileWatcher("test-driver", path, "100ms", onReload)
	if err != nil {
		t.Fatalf("newTagFileWatcher failed: %v", err)
	}
	defer w.stop()

	// Wait a bit to ensure the watcher has ticked at least once with no change.
	time.Sleep(250 * time.Millisecond)
	if reloadCount.Load() != 0 {
		t.Fatalf("expected 0 reloads before change, got %d", reloadCount.Load())
	}

	// Modify the file: add a third tag.
	updated := `
- name: temperature
  address: "40001"
  type: float32
  interval: 1s
- name: pressure
  address: "40003"
  type: float32
  interval: 1s
- name: humidity
  address: "40005"
  type: float32
  interval: 2s
`
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait for the watcher to detect the change.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reloadCount.Load() >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if reloadCount.Load() < 1 {
		t.Fatalf("expected at least 1 reload after change, got %d", reloadCount.Load())
	}
	lastTagsMu.Lock()
	gotLen := len(lastTags)
	gotName := ""
	if gotLen > 2 {
		gotName = lastTags[2].Name
	}
	lastTagsMu.Unlock()
	if gotLen != 3 {
		t.Fatalf("expected 3 tags after reload, got %d", gotLen)
	}
	if gotName != "humidity" {
		t.Errorf("tag[2] name = %q, want %q", gotName, "humidity")
	}
}

func TestTagFileWatcherNoChangeNoReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tags.yaml")
	content := `
- name: temp
  address: "40001"
  type: float32
  interval: 1s
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var reloadCount atomic.Int32
	onReload := func(tags []core.TagConfig) error {
		reloadCount.Add(1)
		return nil
	}

	w, err := newTagFileWatcher("test-driver", path, "100ms", onReload)
	if err != nil {
		t.Fatalf("newTagFileWatcher failed: %v", err)
	}
	defer w.stop()

	// Let the watcher tick several times without changing the file.
	time.Sleep(500 * time.Millisecond)
	if reloadCount.Load() != 0 {
		t.Fatalf("expected 0 reloads with no file change, got %d", reloadCount.Load())
	}
}

func TestTagFileWatcherBadInterval(t *testing.T) {
	_, err := newTagFileWatcher("test", "/dev/null", "not-a-duration", func([]core.TagConfig) error { return nil })
	if err == nil {
		t.Fatal("expected error for invalid interval, got nil")
	}
}
