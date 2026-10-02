package engine

import (
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/core"
)

// ── Tag-File Watcher helpers ────────────────────────────────────────

// startTagFileWatcher starts a hot-reload watcher for a driver's tags-file
// if both TagsFile and TagsInterval are configured.
func (e *CoreCEngine) startTagFileWatcher(config core.DriverConfig) {
	if config.TagsFile == "" || config.TagsInterval == "" {
		return
	}

	// The reload callback replaces the driver's tags with the file content.
	// It rebuilds the DriverConfig with the new tags and swaps the driver
	// in place.
	//
	// IMPORTANT: This callback runs inside the watcher's own loop() goroutine.
	// We must NOT call RemoveDriver() here because RemoveDriver →
	// stopTagFileWatcher → w.stop() → <-w.done would deadlock (loop() is
	// blocked in this callback and can never close done). We must also NOT
	// call AddDriver(), because AddDriver → startTagFileWatcher would start
	// a SECOND watcher for the same driver. That second watcher's goroutine
	// could never be stopped (the old watcher, having deleted itself from
	// the map, would no longer be reachable by stopAllTagFileWatchers),
	// leaking a goroutine on every reload.
	//
	// Instead we call reloadDriverTags(), which swaps the driver underneath
	// the SAME watcher. The watcher stays in tagFileWatchers, its goroutine
	// keeps running, and no new watcher is started — so no goroutine leaks.
	driverName := config.Name
	onReload := func(newTags []core.TagConfig) error {
		e.mu.RLock()
		baseCfg, ok := e.driverConfigs[driverName]
		e.mu.RUnlock()
		if !ok {
			return fmt.Errorf("driver %s config not found during tag-file reload", driverName)
		}

		// Build updated config: new file tags as the complete replacement
		// (file is the source of truth).
		updatedCfg := baseCfg
		updatedCfg.Tags = newTags

		slog.Info("reloading driver from tags-file",
			"driver", driverName, "old_tags", len(baseCfg.Tags), "new_tags", len(newTags))

		if err := e.reloadDriverTags(updatedCfg); err != nil {
			return fmt.Errorf("failed to reload driver with new tags: %w", err)
		}
		return nil
	}

	w, err := newTagFileWatcher(config.Name, config.TagsFile, config.TagsInterval, onReload)
	if err != nil {
		slog.Error("failed to start tag-file watcher",
			"driver", config.Name, "path", config.TagsFile, "error", err)
		return
	}

	e.mu.Lock()
	e.tagFileWatchers[config.Name] = w
	e.mu.Unlock()
}

// reloadDriverTags swaps a driver's tags in place without starting a new
// tag-file watcher. It is used by the tag-file reload callback (see
// startTagFileWatcher) so the existing watcher goroutine keeps running and
// stays in tagFileWatchers — avoiding a goroutine leak on every reload.
//
// It mirrors AddDriver but deliberately skips startTagFileWatcher and
// never touches the tagFileWatchers map. The existing watcher continues
// to poll the same file and will fire this same swap on the next change.
func (e *CoreCEngine) reloadDriverTags(config core.DriverConfig) error {
	// Create and start the new driver first. If this fails, the old
	// driver is left untouched in the maps.
	driver, err := core.CreateDriver(config)
	if err != nil {
		return fmt.Errorf("failed to create driver %s: %w", config.Name, err)
	}
	if err := driver.Init(e.ctx, config); err != nil {
		return fmt.Errorf("failed to init driver %s: %w", config.Name, err)
	}
	if err := driver.Start(e.ctx); err != nil {
		return fmt.Errorf("failed to start driver %s: %w", config.Name, err)
	}

	// Inline teardown of the old driver. We do NOT delete from
	// tagFileWatchers or call stopTagFileWatcher: the running watcher
	// goroutine (which is executing the callback that called us) must
	// remain in the map so Stop() can still reach it and so it can
	// detect future changes. Calling w.stop() here would deadlock
	// (w.stop() waits for loop(), which is blocked in us).
	e.mu.Lock()
	oldDriver, ok := e.drivers[config.Name]
	if ok {
		delete(e.drivers, config.Name)
		delete(e.tagGroups, config.Name)
	}
	e.mu.Unlock()

	if ok {
		if e.scheduler != nil {
			e.scheduler.RemoveDriverTasks(config.Name)
		}
		if err := oldDriver.Stop(); err != nil {
			slog.Error("failed to stop old driver for tag reload",
				"driver", config.Name, "error", err)
		}
	}

	// Install the new driver and refresh its config + tag-group map.
	e.mu.Lock()
	e.drivers[config.Name] = driver
	tagGroups := make(map[string]string, len(config.Tags))
	for _, t := range config.Tags {
		if t.Group != "" {
			tagGroups[t.Name] = t.Group
		}
	}
	e.tagGroups[config.Name] = tagGroups
	e.driverConfigs[config.Name] = config
	e.mu.Unlock()

	// Schedule collection tasks for the new tags.
	e.scheduleDriverTags(config)

	slog.Info("driver reloaded from tags-file",
		"name", config.Name, "type", config.Type, "tags", len(config.Tags))
	return nil
}

// stopTagFileWatcher stops the watcher for a specific driver if one exists.
func (e *CoreCEngine) stopTagFileWatcher(name string) {
	e.mu.Lock()
	w, ok := e.tagFileWatchers[name]
	if ok {
		delete(e.tagFileWatchers, name)
	}
	e.mu.Unlock()
	if ok {
		w.stop()
	}
}

// stopAllTagFileWatchers stops all active tag-file watchers.
func (e *CoreCEngine) stopAllTagFileWatchers() {
	e.mu.Lock()
	watchers := make([]*tagFileWatcher, 0, len(e.tagFileWatchers))
	for name, w := range e.tagFileWatchers {
		watchers = append(watchers, w)
		delete(e.tagFileWatchers, name)
	}
	e.mu.Unlock()
	for _, w := range watchers {
		w.stop()
	}
}
