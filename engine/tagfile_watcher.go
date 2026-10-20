package engine

import (
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/core"
)

// ── Tag-File Watcher helpers ────────────────────────────────────────

// startTagFileWatcher starts a hot-reload watcher for a driver's tags-file
// if both TagsFile and TagsInterval are configured.
func (r *driverRegistry) startTagFileWatcher(config core.DriverConfig) {
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
		r.eng.mu.RLock()
		baseCfg, ok := r.driverConfigs[driverName]
		r.eng.mu.RUnlock()
		if !ok {
			return fmt.Errorf("driver %s config not found during tag-file reload", driverName)
		}

		// Build updated config: new file tags as the complete replacement
		// (file is the source of truth).
		updatedCfg := baseCfg
		updatedCfg.Tags = newTags

		slog.Info("reloading driver from tags-file",
			"driver", driverName, "old_tags", len(baseCfg.Tags), "new_tags", len(newTags))

		// Evict tags that disappeared from the file (audit finding
		// PIPE-1): the file is the source of truth, so removed tags'
		// cached values must go with them — otherwise /tags keeps
		// serving ghosts and derived tags keep consuming them.
		newNames := make(map[string]bool, len(newTags))
		for _, t := range newTags {
			newNames[t.Name] = true
		}
		for _, t := range baseCfg.Tags {
			if !newNames[t.Name] {
				r.eng.processing.cache.DeleteTag(driverName, t.Name)
				slog.Info("evicted cached value for removed tag", "driver", driverName, "tag", t.Name)
			}
		}

		if err := r.reloadDriverTags(updatedCfg); err != nil {
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

	r.eng.mu.Lock()
	r.tagFileWatchers[config.Name] = w
	r.eng.mu.Unlock()
}

// reloadDriverTags swaps a driver's tags in place without starting a new
// tag-file watcher. It is used by the tag-file reload callback (see
// startTagFileWatcher) so the existing watcher goroutine keeps running and
// stays in tagFileWatchers — avoiding a goroutine leak on every reload.
//
// It mirrors AddDriver but deliberately skips startTagFileWatcher and
// never touches the tagFileWatchers map. The existing watcher continues
// to poll the same file and will fire this same swap on the next change.
func (r *driverRegistry) reloadDriverTags(config core.DriverConfig) error {
	// Create and start the new driver first. If this fails, the old
	// driver is left untouched in the maps.
	driver, err := core.CreateDriver(config)
	if err != nil {
		return fmt.Errorf("failed to create driver %s: %w", config.Name, err)
	}
	if err := driver.Init(r.eng.runtime.ctx, config); err != nil {
		return fmt.Errorf("failed to init driver %s: %w", config.Name, err)
	}
	if err := driver.Start(r.eng.runtime.ctx); err != nil {
		return fmt.Errorf("failed to start driver %s: %w", config.Name, err)
	}

	// Inline teardown of the old driver. We do NOT delete from
	// tagFileWatchers or call stopTagFileWatcher: the running watcher
	// goroutine (which is executing the callback that called us) must
	// remain in the map so Stop() can still reach it and so it can
	// detect future changes. Calling w.stop() here would deadlock
	// (w.stop() waits for loop(), which is blocked in us).
	r.eng.mu.Lock()
	oldDriver, ok := r.drivers[config.Name]
	if ok {
		delete(r.drivers, config.Name)
		delete(r.tagGroups, config.Name)
	}
	r.eng.mu.Unlock()

	if ok {
		if r.eng.lifecycle.scheduler != nil {
			r.eng.lifecycle.scheduler.RemoveDriverTasks(config.Name)
		}
		if err := oldDriver.Stop(); err != nil {
			slog.Error("failed to stop old driver for tag reload",
				"driver", config.Name, "error", err)
		}
	}

	// Install the new driver and refresh its config + tag-group map.
	r.eng.mu.Lock()
	r.drivers[config.Name] = driver
	tagGroups := make(map[string]string, len(config.Tags))
	for _, t := range config.Tags {
		if t.Group != "" {
			tagGroups[t.Name] = t.Group
		}
	}
	r.tagGroups[config.Name] = tagGroups
	r.driverConfigs[config.Name] = config
	r.eng.mu.Unlock()

	// Schedule collection tasks for the new tags.
	r.scheduleDriverTags(config)

	slog.Info("driver reloaded from tags-file",
		"name", config.Name, "type", config.Type, "tags", len(config.Tags))
	return nil
}

// stopTagFileWatcher stops the watcher for a specific driver if one exists.
func (r *driverRegistry) stopTagFileWatcher(name string) {
	r.eng.mu.Lock()
	w, ok := r.tagFileWatchers[name]
	if ok {
		delete(r.tagFileWatchers, name)
	}
	r.eng.mu.Unlock()
	if ok {
		w.stop()
	}
}

// stopAllTagFileWatchers stops all active tag-file watchers.
func (r *driverRegistry) stopAllTagFileWatchers() {
	r.eng.mu.Lock()
	watchers := make([]*tagFileWatcher, 0, len(r.tagFileWatchers))
	for name, w := range r.tagFileWatchers {
		watchers = append(watchers, w)
		delete(r.tagFileWatchers, name)
	}
	r.eng.mu.Unlock()
	for _, w := range watchers {
		w.stop()
	}
}
