// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/common/trace"
	"github.com/CoreC-Dev/CoreC/core"
)

// lifecycleSupport groups the background collection and topology
// components: the tag-collection scheduler, the topology auto-discovery
// module, the derived-tag evaluator, and the scheduler tuning windows.
// Its read/data callbacks (readFromDriver/onDriverData) are the plumbing
// the scheduler invokes. Extracted from CoreCEngine in the CPLX-020 God
// Object split; CoreCEngine drives it from its lifecycle methods.
type lifecycleSupport struct {
	scheduler *scheduler

	// discovery handles topology auto-discovery via MQTT heartbeats.
	// nil when node config is not set (auto-discovery disabled).
	discovery *Discovery

	// derivedEvaluator periodically computes derived tags from the latest
	// cache and pushes results into the data bus. nil when no derived tags
	// are configured.
	derivedEvaluator *derivedTagEvaluator

	// errorThrottleWin is the time window within which repeated
	// error/overrun log messages from the scheduler are suppressed.
	errorThrottleWin time.Duration

	// defaultTagInterval is the fallback collection interval for tags
	// that do not specify their own interval.
	defaultTagInterval time.Duration

	// eng is the owning engine: the callbacks reach the driver registry,
	// data bus, stats counters, and shared lock through it; the starters
	// reach the lifecycle context and wait group.
	eng *CoreCEngine
}

// startScheduler creates the data bus and scheduler, then starts the
// scheduler goroutine.
func (l *lifecycleSupport) startScheduler() error {
	l.eng.mu.Lock()
	l.eng.processing.dataBus = NewDataBus(l.eng.processing.dataBusSize)
	l.scheduler = NewScheduler(l.readFromDriver, l.onDriverData, l.errorThrottleWin, l.eng.stats.statManager)
	l.eng.mu.Unlock()

	if err := l.scheduler.Start(l.eng.runtime.ctx); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}
	return nil
}

// startDerivedTags compiles and launches the derived tag evaluator. The
// evaluator reads the latest cache and pushes computed points into the data
// bus. Each derived tag runs in its own goroutine, tracked by the
// evaluator's own wait group (hot-reload support).
func (l *lifecycleSupport) startDerivedTags(config *core.Config) error {
	if err := validateDerivedTags(config.DerivedTags); err != nil {
		return fmt.Errorf("derived tags config: %w", err)
	}
	ev, err := newDerivedTagEvaluator(config.DerivedTags, l.eng.processing.cache, l.eng.processing.dataBus)
	if err != nil {
		return fmt.Errorf("derived tags: %w", err)
	}
	l.swapDerivedEvaluator(ev)
	ev.start(l.eng.runtime.ctx)
	slog.Info("derived tag evaluator started", "count", len(config.DerivedTags))
	return nil
}

// applyDerivedTags hot-swaps the derived tag set on a config reload
// (audit finding CFG-1: derived-tags changes were previously silently
// ignored — PUT returned success, nothing happened). The new set is
// validated and compiled BEFORE the old evaluator is stopped, so a
// broken expression keeps the previous tags running (fail-safe swap);
// the old goroutines are joined before the new ones start, so exactly
// one evaluator is live at any moment.
func (l *lifecycleSupport) applyDerivedTags(configs []core.DerivedTagConfig) error {
	if l.eng.runtime.ctx == nil {
		return fmt.Errorf("engine not started: derived tags can only be applied to a running engine")
	}
	if err := validateDerivedTags(configs); err != nil {
		return fmt.Errorf("derived tags config: %w", err)
	}
	ev, err := newDerivedTagEvaluator(configs, l.eng.processing.cache, l.eng.processing.dataBus)
	if err != nil {
		return fmt.Errorf("derived tags: %w", err)
	}
	old := l.swapDerivedEvaluator(ev)
	if old != nil {
		old.stop()
	}
	ev.start(l.eng.runtime.ctx)
	slog.Info("derived tags applied", "count", len(configs))
	return nil
}

// swapDerivedEvaluator atomically installs the given evaluator (may be
// nil to detach the current one, e.g. at engine shutdown) and returns
// the previously active evaluator so the caller can stop it outside
// any engine lock.
func (l *lifecycleSupport) swapDerivedEvaluator(ev *derivedTagEvaluator) *derivedTagEvaluator {
	l.eng.mu.Lock()
	old := l.derivedEvaluator
	l.derivedEvaluator = ev
	l.eng.mu.Unlock()
	return old
}

// startDiscovery launches the topology auto-discovery module if node config
// is present. It collects the broker URLs from MQTT transports and derives
// the publish endpoint (subscription pattern) from each transport's topic-template.
func (l *lifecycleSupport) startDiscovery(config *core.Config) {
	if config.Node.ID == "" {
		return // auto-discovery disabled
	}

	// Collect broker endpoints from MQTT transports.
	brokerMap := make(map[string]*brokerEndpoint) // deduplicate by broker URL
	for _, tc := range config.Transports {
		if tc.Type != "mqtt" {
			continue
		}
		broker, ok := tc.Settings["broker"].(string)
		if !ok || broker == "" {
			continue
		}

		be, exists := brokerMap[broker]
		if !exists {
			be = &brokerEndpoint{Broker: broker}
			brokerMap[broker] = be
		}

		// If this transport has a topic-template (explicit or auto-filled),
		// it's an outbound transport — advertise a publish endpoint.
		if topicTpl, ok := tc.Settings["topic-template"].(string); ok && topicTpl != "" {
			be.Publish = &endpoint{
				Type:  "mqtt",
				Topic: topicTemplateToSubscriptionPattern(topicTpl),
			}
		}

		// HTTP receive endpoint discovery (webhook-addr) is deferred to a
		// future iteration. When implemented, it will be collected from
		// HTTP transports in a separate loop and paired with the broker
		// used for discovery heartbeats.
	}

	if len(brokerMap) == 0 {
		slog.Warn("discovery: node config set but no MQTT transports found, auto-discovery disabled",
			"node", config.Node.ID)
		return
	}

	brokers := make([]brokerEndpoint, 0, len(brokerMap))
	for _, be := range brokerMap {
		brokers = append(brokers, *be)
	}

	l.discovery = NewDiscovery(config.Node, brokers, l.eng.AddTransport)
	if err := l.discovery.Start(l.eng.runtime.ctx); err != nil {
		slog.Error("failed to start discovery, continuing without auto-discovery", "error", err)
		l.discovery = nil
	}
}

// readFromDriver is the scheduler's read callback: it resolves the driver
// from the registry and reads the requested tags, observing read latency.
func (l *lifecycleSupport) readFromDriver(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
	ctx, span := trace.Start(ctx, "engine.readFromDriver")
	defer span.End()
	span.SetAttr("driver", driver)
	span.SetAttr("tag_count", len(tags))

	l.eng.mu.RLock()
	d, ok := l.eng.driverReg.drivers[driver]
	l.eng.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver not found: %s", driver)
	}
	start := time.Now()
	values, err := d.Read(ctx, tags)
	// Observe read latency in both the global aggregate histogram and
	// the per-driver labeled histogram (audit OBS-2: a single slow
	// driver must be distinguishable from healthy siblings).
	l.eng.stats.labeled.observeRead(driver, time.Since(start))
	return values, err
}

// onDriverData is the scheduler's data callback: it enriches each value
// with its tag group and pushes it into the data bus (high-priority
// channel for fast-interval tasks).
func (l *lifecycleSupport) onDriverData(driver string, values []core.TagValue, priority int) {
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
	l.eng.mu.RLock()
	groups := l.eng.driverReg.tagGroups[driver]
	l.eng.mu.RUnlock()

	// Route to the high-priority channel when the task's priority is > 0
	// (fast-interval tasks). Dedicated high-priority workers process this
	// channel so a burst of low-frequency bulk reads cannot delay
	// high-frequency collection (IMPROVEMENTS #5).
	highPriority := priority > 0

	for _, v := range values {
		l.eng.stats.recordRead(1)

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
			l.eng.processing.dataBus.PushHighPriority(point)
		} else {
			l.eng.processing.dataBus.Push(point)
		}
	}
}
