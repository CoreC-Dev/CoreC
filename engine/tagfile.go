package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/goccy/go-yaml"
)

// tagFileWatcher periodically re-reads a driver's tags-file and invokes
// a callback when the file content changes. It mirrors the reload-loop
// pattern used by rule.FileProvider.
type tagFileWatcher struct {
	driverName string
	path       string
	interval   time.Duration

	// onReload is called with the new tags when the file changes.
	// It receives the full replacement tag list (file tags only).
	onReload func([]core.TagConfig) error

	mu     sync.Mutex
	hash   string // SHA-256 of last successfully loaded file content
	stopCh chan struct{}
	done   chan struct{}
}

// newTagFileWatcher creates a watcher. The first load happens immediately
// in the constructor; if it fails the error is returned and no watcher
// goroutine is started.
func newTagFileWatcher(driverName, path, intervalStr string, onReload func([]core.TagConfig) error) (*tagFileWatcher, error) {
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		return nil, fmt.Errorf("invalid tags-interval %q: %w", intervalStr, err)
	}
	if interval <= 0 {
		return nil, fmt.Errorf("tags-interval must be positive, got %v", interval)
	}

	w := &tagFileWatcher{
		driverName: driverName,
		path:       path,
		interval:   interval,
		onReload:   onReload,
		stopCh:     make(chan struct{}),
		done:       make(chan struct{}),
	}

	// Initial load to compute the baseline hash. We don't call onReload
	// here because the driver was already started with the file's tags
	// during config parsing. We only need the hash for change detection.
	tags, hash, err := w.load()
	if err != nil {
		return nil, fmt.Errorf("driver %s: tags-file initial load failed: %w", driverName, err)
	}
	w.hash = hash
	slog.Info("tags-file watcher initialised",
		"driver", driverName, "path", path, "tags", len(tags), "interval", interval)

	go w.loop()
	return w, nil
}

// load reads the tags file, parses it, and returns the tags plus a hash
// of the raw file content for change detection.
func (w *tagFileWatcher) load() ([]core.TagConfig, string, error) {
	data, err := os.ReadFile(w.path)
	if err != nil {
		return nil, "", fmt.Errorf("read file %s: %w", w.path, err)
	}

	var tags []core.TagConfig
	if err := yaml.Unmarshal(data, &tags); err != nil {
		return nil, "", fmt.Errorf("parse yaml %s: %w", w.path, err)
	}

	sum := sha256.Sum256(data)
	return tags, hex.EncodeToString(sum[:]), nil
}

// loop periodically checks the file and triggers onReload on change.
func (w *tagFileWatcher) loop() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := w.checkAndReload(); err != nil {
				slog.Error("tags-file reload failed",
					"driver", w.driverName, "path", w.path, "error", err)
			}
		case <-w.stopCh:
			return
		}
	}
}

// checkAndReload reads the file and calls onReload if the content hash
// changed since the last successful load.
func (w *tagFileWatcher) checkAndReload() error {
	tags, hash, err := w.load()
	if err != nil {
		return err
	}

	w.mu.Lock()
	if hash == w.hash {
		w.mu.Unlock()
		return nil // unchanged
	}
	w.hash = hash
	w.mu.Unlock()

	slog.Info("tags-file changed, reloading",
		"driver", w.driverName, "path", w.path, "tags", len(tags))

	if err := w.onReload(tags); err != nil {
		return fmt.Errorf("reload callback: %w", err)
	}
	return nil
}

// stop signals the watcher goroutine to exit and waits for it.
func (w *tagFileWatcher) stop() {
	close(w.stopCh)
	<-w.done
}
