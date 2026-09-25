package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/common/trace"
	"github.com/CoreC-Dev/CoreC/core"
)

// --- Driver Management ---

func (e *CoreCEngine) AddDriver(config core.DriverConfig) error {
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

	e.mu.Lock()
	// If a driver with the same name already exists (e.g. via direct API call),
	// remove it from the map now so concurrent callers don't see a stopping
	// driver. The actual Stop() happens after Unlock to avoid blocking the lock.
	var oldDriver core.Driver
	if d, exists := e.drivers[config.Name]; exists {
		oldDriver = d
		delete(e.drivers, config.Name)
		delete(e.tagGroups, config.Name)
	}
	e.mu.Unlock()

	// Stop the old driver outside the lock — it's already removed from the
	// map, so no concurrent caller can interact with it.
	if oldDriver != nil {
		if e.scheduler != nil {
			if err := e.scheduler.PauseDriver(config.Name); err != nil {
				slog.Error("failed to pause old driver in scheduler", "name", config.Name, "error", err)
			}
		}
		if err := oldDriver.Stop(); err != nil {
			slog.Error("failed to stop old driver on replacement", "name", config.Name, "error", err)
		}
	}

	e.mu.Lock()
	e.drivers[config.Name] = driver

	// Build tag -> group map for DataPoint.Group enrichment in onDriverData.
	// Only tags that declare a group are stored, keeping the map small.
	tagGroups := make(map[string]string, len(config.Tags))
	for _, t := range config.Tags {
		if t.Group != "" {
			tagGroups[t.Name] = t.Group
		}
	}
	e.tagGroups[config.Name] = tagGroups
	e.mu.Unlock()

	// Schedule collection tasks based on tag intervals
	e.scheduleDriverTags(config)

	// Store config for tag-file watcher rebuilds.
	e.mu.Lock()
	e.driverConfigs[config.Name] = config
	e.mu.Unlock()

	// Start tag-file watcher if configured.
	e.startTagFileWatcher(config)

	slog.Info("driver added", "name", config.Name, "type", config.Type, "tags", len(config.Tags))
	return nil
}

func (e *CoreCEngine) RemoveDriver(name string) error {
	// Stop tag-file watcher first so it doesn't try to reload a
	// driver that's being removed.
	e.stopTagFileWatcher(name)

	e.mu.Lock()
	driver, ok := e.drivers[name]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("driver not found: %s", name)
	}
	delete(e.drivers, name)
	delete(e.tagGroups, name)
	delete(e.driverConfigs, name)
	e.mu.Unlock()

	if e.scheduler != nil {
		// Remove all scheduler tasks for this driver so their goroutines
		// actually exit, instead of just pausing them and leaving the
		// goroutines running forever (problem 3).
		e.scheduler.RemoveDriverTasks(name)
	}
	return driver.Stop()
}

func (e *CoreCEngine) GetDriver(name string) (core.Driver, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d, ok := e.drivers[name]
	return d, ok
}

func (e *CoreCEngine) ListDrivers() []core.DriverStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]core.DriverStatus, 0, len(e.drivers))
	for _, d := range e.drivers {
		result = append(result, d.Status())
	}
	return result
}

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
func (e *CoreCEngine) readFromDriver(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
	ctx, span := trace.Start(ctx, "engine.readFromDriver")
	defer span.End()
	span.SetAttr("driver", driver)
	span.SetAttr("tag_count", len(tags))

	e.mu.RLock()
	d, ok := e.drivers[driver]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver not found: %s", driver)
	}
	start := time.Now()
	values, err := d.Read(ctx, tags)
	e.readLatency.Observe(time.Since(start))
	return values, err
}

func (e *CoreCEngine) onDriverData(driver string, values []core.TagValue, priority int) {
	// Enrich DataPoint.Group from the TagConfig.Group captured in AddDriver.
	// TagValue only carries the tag name, so the group must be looked up by
	// (driver, tag). We read the per-driver map once under RLock to avoid
	// per-point lock churn.
	//
	// NOTE on Device: there is currently no Device source in config
	// (DriverConfig has no Device field and TagConfig has no Device field),
	// so DataPoint.Device is left empty here. To populate it, add a Device
	// field to DriverConfig (driver-wide) or TagConfig (per-tag) and store
	// it alongside tagGroups, then set point.Device below.
	e.mu.RLock()
	groups := e.tagGroups[driver]
	e.mu.RUnlock()

	// Route to the high-priority channel when the task's priority is > 0
	// (fast-interval tasks). Dedicated high-priority workers process this
	// channel so a burst of low-frequency bulk reads cannot delay
	// high-frequency collection (IMPROVEMENTS #5).
	highPriority := priority > 0

	for _, v := range values {
		e.totalRead.Add(1)

		point := core.DataPoint{
			Driver:    driver,
			Tag:       v.Tag,
			Value:     v.Value,
			Type:      v.Type,
			Quality:   v.Quality,
			Timestamp: v.Timestamp,
		}
		if groups != nil {
			point.Group = groups[v.Tag]
		}

		if highPriority {
			e.dataBus.PushHighPriority(point)
		} else {
			e.dataBus.Push(point)
		}
	}
}

func (e *CoreCEngine) scheduleDriverTags(config core.DriverConfig) {
	// Group tags by their parsed interval so that equivalent spellings
	// (e.g. "1s" and "1000ms") collapse into a single task instead of
	// creating duplicate tasks with the same cadence.
	intervalGroups := make(map[time.Duration][]string)
	deadBands := make(map[string]float64)
	// readTimeouts tracks the max parsed read-timeout per interval group.
	// Tags in the same group are read together in a single readFunc call,
	// so the effective timeout is the most generous one requested.
	readTimeouts := make(map[time.Duration]time.Duration)
	for _, tag := range config.Tags {
		interval := e.defaultTagInterval
		if tag.Interval != "" {
			if d, err := time.ParseDuration(tag.Interval); err == nil && d > 0 {
				interval = d
			} else {
				slog.Error("invalid interval", "interval", tag.Interval, "error", err)
			}
		}
		intervalGroups[interval] = append(intervalGroups[interval], tag.Name)
		if tag.DeadBand > 0 {
			deadBands[tag.Name] = tag.DeadBand
		}
		if tag.ReadTimeout != "" {
			if d, err := time.ParseDuration(tag.ReadTimeout); err == nil && d > 0 {
				if d > readTimeouts[interval] {
					readTimeouts[interval] = d
				}
			}
		}
	}

	for interval, tags := range intervalGroups {
		taskID := fmt.Sprintf("%s_%s", config.Name, interval.String())
		// Classify as high-priority when the collection interval is at or
		// below the threshold. Fast-interval tasks get dedicated workers so
		// a burst of low-frequency bulk reads cannot delay them
		// (IMPROVEMENTS #5).
		priority := 0
		if interval > 0 && interval <= core.DefaultHighPriorityInterval {
			priority = 1
		}
		if err := e.scheduler.AddTask(core.ScheduleTask{
			ID:          taskID,
			Driver:      config.Name,
			Tags:        tags,
			Interval:    interval,
			Priority:    priority,
			DeadBands:   deadBands,
			ReadTimeout: readTimeouts[interval],
		}); err != nil {
			slog.Error("failed to add schedule task", "task", taskID, "error", err)
		}
	}
}
