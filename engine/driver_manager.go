package engine

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
)

// loggerInjector is an optional interface that drivers implement to
// receive a structured Logger. The engine calls SetLogger during
// AddDriver if the driver supports it.
type loggerInjector interface {
	SetLogger(core.Logger)
}

// metricsInjector is an optional interface that drivers implement to
// receive a Metrics backend. The engine calls SetMetrics during
// AddDriver if the driver supports it.
type metricsInjector interface {
	SetMetrics(core.Metrics)
}

// driverRegistry groups the southbound driver concern: the live driver
// map, the last-applied driver configs (for tag-file watcher rebuilds),
// the per-driver tag-file watchers, and the tag-group enrichment map.
// CoreCEngine keeps thin forwarders to this role so the
// core.DriverManager method set is unchanged.
type driverRegistry struct {
	drivers map[string]core.Driver

	// driverConfigs stores the last-applied DriverConfig per driver name,
	// so tag-file watchers can rebuild the config with updated tags.
	driverConfigs map[string]core.DriverConfig

	// tagFileWatchers tracks per-driver hot-reload watchers for tags-file.
	// Keyed by driver name; nil entry means no watcher for that driver.
	tagFileWatchers map[string]*tagFileWatcher

	// tagGroups maps driver name -> tag name -> group.
	// Populated from TagConfig.Group in AddDriver and consulted in
	// onDriverData to enrich DataPoint.Group. The scheduler only forwards
	// []TagValue (which carries the tag name but not its group), so
	// without this map the Group would always be empty — breaking rules
	// like "group == 'reactor'" and MQTT topic templates like
	// ".../{{.Group}}/...".
	tagGroups map[string]map[string]string

	// eng is the owning engine: AddDriver/RemoveDriver reach the shared
	// lock, lifecycle context, scheduler, and default tag interval
	// through it.
	eng *CoreCEngine
}

// --- Driver Management ---

func (r *driverRegistry) AddDriver(config core.DriverConfig) error {
	driver, err := core.CreateDriver(config)
	if err != nil {
		return fmt.Errorf("failed to create driver %s: %w", config.Name, err)
	}

	// Inject Logger and Metrics if the driver supports it. This decouples
	// drivers from the global slog/Prometheus backends, enabling independent
	// testing and community reuse with custom backends. Drivers that don't
	// implement SetLogger/SetMetrics (e.g. future or third-party drivers)
	// simply skip injection and use their internal defaults.
	if li, ok := driver.(loggerInjector); ok {
		li.SetLogger(log.DefaultLogger())
	}
	if mi, ok := driver.(metricsInjector); ok {
		// The engine's registry, not NoopMetrics: protocol-level driver
		// metrics (modbus exception codes, ...) flow to /metrics via the
		// core.DriverMetricsProvider role.
		mi.SetMetrics(core.Metrics(r.eng.stats.driverMetrics))
	}

	if err := driver.Init(r.eng.runtime.ctx, config); err != nil {
		return fmt.Errorf("failed to init driver %s: %w", config.Name, err)
	}

	if err := driver.Start(r.eng.runtime.ctx); err != nil {
		return fmt.Errorf("failed to start driver %s: %w", config.Name, err)
	}

	r.eng.mu.Lock()
	// If a driver with the same name already exists (e.g. via direct API call),
	// remove it from the map now so concurrent callers don't see a stopping
	// driver. The actual Stop() happens after Unlock to avoid blocking the lock.
	var oldDriver core.Driver
	if d, exists := r.drivers[config.Name]; exists {
		oldDriver = d
		delete(r.drivers, config.Name)
		delete(r.tagGroups, config.Name)
	}
	r.eng.mu.Unlock()

	// Stop the old driver outside the lock — it's already removed from the
	// map, so no concurrent caller can interact with it.
	if oldDriver != nil {
		if r.eng.lifecycle.scheduler != nil {
			if err := r.eng.lifecycle.scheduler.PauseDriver(config.Name); err != nil {
				slog.Error("failed to pause old driver in scheduler", "name", config.Name, "error", err)
			}
		}
		if err := oldDriver.Stop(); err != nil {
			slog.Error("failed to stop old driver on replacement", "name", config.Name, "error", err)
		}
	}

	r.eng.mu.Lock()
	r.drivers[config.Name] = driver

	// Build tag -> group map for DataPoint.Group enrichment in onDriverData.
	// Only tags that declare a group are stored, keeping the map small.
	tagGroups := make(map[string]string, len(config.Tags))
	for i := range config.Tags {
		t := config.Tags[i]
		if t.Group != "" {
			tagGroups[t.Name] = t.Group
		}
	}
	r.tagGroups[config.Name] = tagGroups
	r.eng.mu.Unlock()

	// Schedule collection tasks based on tag intervals
	r.scheduleDriverTags(config)

	// Store config for tag-file watcher rebuilds.
	r.eng.mu.Lock()
	r.driverConfigs[config.Name] = config
	r.eng.mu.Unlock()

	// Start tag-file watcher if configured.
	r.startTagFileWatcher(config)

	slog.Info("driver added", "name", config.Name, "type", config.Type, "tags", len(config.Tags))
	return nil
}

func (r *driverRegistry) RemoveDriver(name string) error {
	// Stop tag-file watcher first so it doesn't try to reload a
	// driver that's being removed.
	r.stopTagFileWatcher(name)

	r.eng.mu.Lock()
	driver, ok := r.drivers[name]
	if !ok {
		r.eng.mu.Unlock()
		return fmt.Errorf("driver not found: %s", name)
	}
	delete(r.drivers, name)
	delete(r.tagGroups, name)
	delete(r.driverConfigs, name)
	r.eng.mu.Unlock()

	r.eng.writes.pacer.forget(name)

	// Evict the driver's cached values: without this the cache keeps
	// serving a deleted driver's last values in
	// GET /tags forever (stale-flagged, but never gone) and derived
	// tags keep consuming the ghosts.
	r.eng.processing.cache.DeleteDriver(name)

	if r.eng.lifecycle.scheduler != nil {
		// Remove all scheduler tasks for this driver so their goroutines
		// actually exit, instead of just pausing them and leaving the
		// goroutines running forever (problem 3).
		r.eng.lifecycle.scheduler.RemoveDriverTasks(name)
	}
	return driver.Stop()
}

func (r *driverRegistry) GetDriver(name string) (core.Driver, bool) {
	r.eng.mu.RLock()
	defer r.eng.mu.RUnlock()
	d, ok := r.drivers[name]
	return d, ok
}

func (r *driverRegistry) ListDrivers() []core.DriverStatus {
	r.eng.mu.RLock()
	defer r.eng.mu.RUnlock()
	result := make([]core.DriverStatus, 0, len(r.drivers))
	for _, d := range r.drivers {
		result = append(result, d.Status())
	}
	return result
}

func (r *driverRegistry) scheduleDriverTags(config core.DriverConfig) {
	// The driver instance supplies its optional interval floor. Look it up
	// here instead of threading it through the call sites: the tag-file
	// watcher rebuilds tasks without ever holding the driver variable.
	r.eng.mu.RLock()
	inst := r.drivers[config.Name]
	r.eng.mu.RUnlock()

	// Group tags by their parsed interval so that equivalent spellings
	// (e.g. "1s" and "1000ms") collapse into a single task instead of
	// creating duplicate tasks with the same cadence.
	intervalGroups := make(map[time.Duration][]string)
	deadBands := make(map[string]float64)
	enforceIntervals := make(map[string]time.Duration)
	groupTrigger := make(map[string]string) // trigger-group -> condition tag
	gatedTags := make(map[string][]string)  // trigger-group -> gated detail tags
	// readTimeouts tracks the max parsed read-timeout per interval group.
	// Tags in the same group are read together in a single readFunc call,
	// so the effective timeout is the most generous one requested.
	readTimeouts := make(map[time.Duration]time.Duration)
	for i := range config.Tags {
		tag := config.Tags[i]
		// write-only points accept writes but are never collected: skipping
		// them here keeps them out of every read path, the cache and rule
		// input, matching the documented `protect: write-only` contract.
		if tag.Protect == "write-only" {
			continue
		}
		interval := r.eng.lifecycle.defaultTagInterval
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
		if tag.EnforceInterval != "" {
			if d, err := time.ParseDuration(tag.EnforceInterval); err == nil && d > 0 {
				enforceIntervals[tag.Name] = d
			}
		}
		collectTriggerTag(tag, groupTrigger, gatedTags)
		if tag.ReadTimeout != "" {
			if d, err := time.ParseDuration(tag.ReadTimeout); err == nil && d > 0 {
				if d > readTimeouts[interval] {
					readTimeouts[interval] = d
				}
			}
		}
	}

	// Clamp each group's interval up to the driver's declared floor (the
	// optional core.IntervalLimiter role). A configuration that asks for a
	// cadence the protocol cannot sustain (e.g. 10 ms over 9600-baud Modbus
	// RTU) must not silently saturate the device: the task runs at the floor
	// and one WARN per affected group names the tags and both numbers.
	minInterval := driverMinInterval(inst)

	for interval, tags := range intervalGroups {
		effective := interval
		if minInterval > 0 && interval < minInterval {
			effective = minInterval
			slog.Warn("collection interval raised to the driver minimum",
				"driver", config.Name,
				"interval", interval.String(),
				"min_interval", minInterval.String(),
				"tags", tags)
		}
		taskID := fmt.Sprintf("%s_%s", config.Name, effective.String())
		// Classify as high-priority when the collection interval is at or
		// below the threshold. Fast-interval tasks get dedicated workers so
		// a burst of low-frequency bulk reads cannot delay them.
		priority := 0
		if effective > 0 && effective <= core.DefaultHighPriorityInterval {
			priority = 1
		}
		if err := r.eng.lifecycle.scheduler.AddTask(core.ScheduleTask{
			ID:               taskID,
			Driver:           config.Name,
			Tags:             tags,
			Interval:         effective,
			Priority:         priority,
			DeadBands:        deadBands,
			EnforceIntervals: enforceIntervals,
			GroupTrigger:     groupTrigger,
			GroupGated:       gatedTags,
			ReadTimeout:      readTimeouts[interval],
		}); err != nil {
			slog.Error("failed to add schedule task", "task", taskID, "error", err)
		}
	}
}

// hasDriver reports whether a driver with the given name is registered.
func (r *driverRegistry) hasDriver(name string) bool {
	r.eng.mu.RLock()
	defer r.eng.mu.RUnlock()
	_, ok := r.drivers[name]
	return ok
}

// startDrivers initializes and starts all configured drivers.
func (r *driverRegistry) startDrivers(config *core.Config) error {
	for _, dc := range config.Drivers {
		if err := r.AddDriver(dc); err != nil {
			slog.Error("failed to add driver", "name", dc.Name, "error", err)
			return fmt.Errorf("failed to add driver %s: %w", dc.Name, err)
		}
	}
	return nil
}

// --- CoreCEngine forwarders (core.DriverManager) ---

func (e *CoreCEngine) AddDriver(config core.DriverConfig) error {
	return e.driverReg.AddDriver(config)
}

func (e *CoreCEngine) RemoveDriver(name string) error {
	return e.driverReg.RemoveDriver(name)
}

func (e *CoreCEngine) GetDriver(name string) (core.Driver, bool) {
	return e.driverReg.GetDriver(name)
}

func (e *CoreCEngine) ListDrivers() []core.DriverStatus {
	return e.driverReg.ListDrivers()
}

// driverMinInterval returns the minimum collection interval a driver declares
// through the optional core.IntervalLimiter role, or 0 when the driver does not
// implement the role (or declares no meaningful floor). Keeping the probe here
// — rather than in core.DriverCapabilities — means drivers opt in without the
// engine contract changing.
func driverMinInterval(d core.Driver) time.Duration {
	limiter, ok := d.(core.IntervalLimiter)
	if !ok || limiter == nil {
		return 0
	}
	if floor := limiter.MinInterval(); floor > 0 {
		return floor
	}
	return 0
}

// collectTriggerTag records a tag's role in trigger groups: a condition point
// declares the group (GroupTrigger -> condition tag), a detail point declares
// the condition that gates it (GroupGated -> gated tags). Kept out of
// scheduleDriverTags to hold that function under the cyclomatic ceiling.
func collectTriggerTag(tag core.TagConfig, groupTrigger map[string]string, gatedTags map[string][]string) {
	if tag.TriggerGroup != "" {
		groupTrigger[tag.TriggerGroup] = tag.Name
	}
	if tag.Triggers != "" {
		gatedTags[tag.Triggers] = append(gatedTags[tag.Triggers], tag.Name)
	}
}
