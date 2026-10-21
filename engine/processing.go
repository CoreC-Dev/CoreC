// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/rule"
)

// processingCore groups the data-plane concern: the internal data bus,
// the latest-value cache, the alert handlers, the bad-quality policy,
// and the processing parallelism tuning. Its worker loops consume the
// bus, run the rule pipeline, and fan points out to transports.
// Extracted from CoreCEngine in the CPLX-020 God Object split;
// CoreCEngine keeps thin forwarders so the core.EventSubscriber method
// set is unchanged.
type processingCore struct {
	dataBus *DataBus
	cache   *LatestCache

	// Alert handlers
	alertHandlers []func(point core.DataPoint, rule core.Rule)

	// alerts is the first-class alert registry (audit findings
	// OBS-4/HUB-6: alerts previously degenerated to a log line). It is
	// re-created by every engine Start alongside the other pipeline
	// state; nil-safe guards keep direct processPoint calls (tests)
	// working on unstarted engines.
	alerts *alertManager

	// alertActiveWindow is the alert lifetime after its last fire,
	// parsed from global.engine.alert-active-window (default
	// core.DefaultAlertActiveWindow) and consumed when Start builds the
	// registry.
	alertActiveWindow time.Duration

	// badQualityPolicy controls how DataPoints with QualityBad are
	// handled in the processing pipeline (set from
	// GlobalConfig.Engine.OnBadQuality).
	badQualityPolicy badQualityPolicy

	// Processing parallelism
	numWorkers int

	// dataBusSize is the buffer capacity of the internal data channel
	// connecting drivers to the processing pipeline (set from
	// GlobalConfig.Engine.DataBusSize).
	dataBusSize int

	// numHighPriorityWorkers is the number of dedicated workers that
	// process only high-priority (fast-interval) data from the DataBus
	// high-priority channel. This isolates high-frequency collection from
	// low-frequency bulk-read bursts (IMPROVEMENTS #5).
	numHighPriorityWorkers int

	// eng is the owning engine: the loops reach the lifecycle context,
	// wait group, stats counters, rule set, and transport registry
	// through it.
	eng *CoreCEngine
}

func (p *processingCore) processingLoop() {
	defer p.eng.runtime.wg.Done()

	for {
		select {
		case <-p.eng.runtime.ctx.Done():
			return
		case point, ok := <-p.dataBus.Channel():
			if !ok {
				return
			}
			p.processPoint(point)
		}
	}
}

// processingLoopHighPriority is the dedicated worker for high-priority
// (fast-interval) data. It reads only from the DataBus high-priority
// channel, so a burst of low-frequency bulk reads on the main channel
// cannot delay high-frequency collection (IMPROVEMENTS #5).
func (p *processingCore) processingLoopHighPriority() {
	defer p.eng.runtime.wg.Done()

	for {
		select {
		case <-p.eng.runtime.ctx.Done():
			return
		case point, ok := <-p.dataBus.HighPriorityChannel():
			if !ok {
				return
			}
			p.processPoint(point)
		}
	}
}

// startProcessingWorkers launches the N worker goroutines that consume
// from the DataBus channel, plus optional high-priority workers.
func (p *processingCore) startProcessingWorkers() {
	numWorkers := p.numWorkers
	if numWorkers < 1 {
		numWorkers = 1
	}
	p.eng.runtime.wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go p.processingLoop()
	}

	numHP := p.numHighPriorityWorkers
	if numHP < 0 {
		numHP = 0
	}
	if numHP > 0 {
		p.eng.runtime.wg.Add(numHP)
		for i := 0; i < numHP; i++ {
			go p.processingLoopHighPriority()
		}
		slog.Info("high-priority processing workers started", "count", numHP)
	}
}

// processPoint applies the full processing pipeline to a single DataPoint:
// bad-quality policy, cache update, subscriber broadcast, rule matching,
// and action execution (publish/transform/alert/drop). It is shared by both
// the normal and high-priority processing loops.
func (p *processingCore) processPoint(point core.DataPoint) {
	// Apply bad-quality policy before any further processing.
	// This prevents downstream systems from receiving
	// misleading bad-quality values unless explicitly configured.
	policyAlerted := false
	if point.Quality == core.QualityBad {
		switch p.badQualityPolicy {
		case badQualityDrop:
			// A configured discard policy is not a processing error:
			// count it as a bad-quality drop (OBS-6) so total_errors
			// stops conflating policy with faults.
			p.eng.stats.recordBadQualityDrop()
			return // discard entirely
		case badQualityMarkAndPublish:
			point.Value = nil // keep quality=bad, clear value
		case badQualityAlert:
			// Forward + alert. The alert fires HERE, before rule
			// matching, so it does not depend on any rule matching the
			// bad point (stage-B audit finding B5-1: previously it was
			// raised after the action loop and Match()==0 returned
			// early — with zero rules configured the policy never
			// alerted, violating the documented "publish and trigger
			// the alert callback" contract; a terminal drop rule
			// also skipped it). policyAlerted suppresses the duplicate
			// rule-level alert below, mirroring the old anti-double-
			// alert intent.
			p.fireAlert(point, badQualityAlertRule{})
			policyAlerted = true
		}
	}

	// Update cache
	p.cache.Update(point)

	// Broadcast to subscribers
	p.dataBus.Broadcast(point)

	// Match rules — returns all matching rules in priority order, up to
	// and including the first terminal match (Continue() == false).
	// All rules are matched against the ORIGINAL point; transforms in the
	// action loop below mutate the point for publishing but do not affect
	// which rules matched (see ADR-010 § Limitations).
	results := p.eng.ruleSet.ruleEngine.Match(point)
	if len(results) == 0 {
		return
	}

	// Execute actions for each matched rule in priority order. Non-terminal
	// rules (Continue() == true) apply their side-effect and continue;
	// the last result is always terminal (or the loop ends naturally when
	// no terminal rule matches).
	for _, result := range results {
		ruleName := result.Rule.Name()
		switch result.Rule.Action() {
		case core.ActionDrop:
			return
		case core.ActionAlert:
			// A bad-quality policy alert may already have fired for this
			// point (see the policy switch above); suppress the duplicate
			// but still publish to the rule's targets.
			if !policyAlerted {
				p.fireAlert(point, result.Rule)
			}
			p.publishToTargets(pointWithRule(point, ruleName), result.Targets)
		case core.ActionTransform:
			// Transform mutates the point for subsequent action publishing:
			// a later rule's publishToTargets sees the transformed value.
			// However, the later rule's MATCH was already evaluated against
			// the original point — transforms do not re-trigger matching.
			point = p.applyTransform(point, result.Transform)
			p.publishToTargets(pointWithRule(point, ruleName), result.Targets)
		case core.ActionForward, core.ActionMirror:
			p.publishToTargets(pointWithRule(point, ruleName), result.Targets)
		}
	}
}

// pointWithRule returns a shallow copy of point with the rule name injected
// into Metadata["rule"]. A new Metadata map is allocated so the original
// point is not mutated (avoids races when multiple rules forward the same
// point to different targets via async batchers).
func pointWithRule(point core.DataPoint, ruleName string) core.DataPoint {
	p := point
	p.Metadata = make(map[string]string, len(point.Metadata)+1)
	for k, v := range point.Metadata {
		p.Metadata[k] = v
	}
	p.Metadata["rule"] = ruleName
	return p
}

// badQualityAlertRule is the synthetic rule context passed to alert
// handlers when the on-bad-quality: alert POLICY itself raises the
// alert. Policy alerts are not tied to any configured rule, but
// handlers expect a core.Rule for context, so a named sentinel
// describes the origin (engine-internal; never configurable, never
// matched — Match always returns false and it is never registered in
// the rule engine).
type badQualityAlertRule struct{}

func (badQualityAlertRule) Match(core.DataPoint) bool        { return false }
func (badQualityAlertRule) Action() core.Action              { return core.ActionAlert }
func (badQualityAlertRule) Target() string                   { return "" }
func (badQualityAlertRule) Targets() []string                { return nil }
func (badQualityAlertRule) Priority() int                    { return 0 }
func (badQualityAlertRule) Name() string                     { return "bad-quality-policy" }
func (badQualityAlertRule) Payload() string                  { return "quality == 'bad'" }
func (badQualityAlertRule) RuleType() string                 { return "policy" }
func (badQualityAlertRule) Transform() *core.TransformConfig { return nil }
func (badQualityAlertRule) Continue() bool                   { return false }
func (badQualityAlertRule) String() string                   { return "bad-quality-policy" }

// applyTransform evaluates the transform expression against the data point's
// value and applies the tag rename, returning a new point. If the transform
// config is nil, the expression is empty, or the value is non-numeric, the
// original point is returned unchanged (with a warning logged on eval failure).
func (p *processingCore) applyTransform(point core.DataPoint, tc *core.TransformConfig) core.DataPoint {
	if tc == nil || tc.Expression == "" {
		return point
	}

	raw, ok := util.ToFloat64OK(point.Value)
	if !ok {
		// Only numeric values can be arithmetically transformed.
		return point
	}

	result, err := rule.EvalArith(tc.Expression, raw)
	if err != nil {
		slog.Warn("transform expression eval failed, keeping original value",
			"tag", point.Tag, "expression", tc.Expression, "error", err)
		return point
	}

	out := point
	out.Value = result
	if tc.TagRename != "" {
		out.Tag = tc.TagRename
	}
	return out
}

// publishTargetEntry is a snapshot of a single transport and its optional
// batcher, captured under the engine read lock so that Publish can be
// called outside the lock (problem 8).
type publishTargetEntry struct {
	name      string
	transport core.Transport
	batcher   *transportBatcher // nil if no batcher configured
}

func (p *processingCore) publishToTargets(point core.DataPoint, targets []string) {
	// Snapshot transports and batchers under RLock, then release the
	// lock before calling Publish. This prevents slow network I/O from
	// blocking management operations (AddDriver/RemoveDriver/etc.) that
	// need the write lock (problem 8).
	p.eng.mu.RLock()
	snapshot := make([]publishTargetEntry, 0, len(targets))
	for _, targetName := range targets {
		transport, ok := p.eng.transportMgr.transports[targetName]
		if !ok {
			slog.Warn("target transport not found", "target", targetName)
			p.eng.stats.totalErrors.Add(1)
			continue
		}
		entry := publishTargetEntry{name: targetName, transport: transport}
		if batcher, ok := p.eng.transportMgr.batchers[targetName]; ok {
			entry.batcher = batcher
		}
		snapshot = append(snapshot, entry)
	}

	// Also snapshot fallback transports for failover.
	fallbackSnapshot := make(map[string]publishTargetEntry, len(snapshot))
	for _, entry := range snapshot {
		if cfg, ok := p.eng.transportMgr.transportCfg[entry.name]; ok && cfg.Fallback != "" {
			if ft, ok2 := p.eng.transportMgr.transports[cfg.Fallback]; ok2 {
				fbEntry := publishTargetEntry{name: cfg.Fallback, transport: ft}
				if batcher, ok2 := p.eng.transportMgr.batchers[cfg.Fallback]; ok2 {
					fbEntry.batcher = batcher
				}
				fallbackSnapshot[entry.name] = fbEntry
			}
		}
	}
	p.eng.mu.RUnlock()

	for _, entry := range snapshot {
		// Use batcher if configured, otherwise publish directly
		if entry.batcher != nil {
			// publishLatency and dataAge are observed inside the
			// batcher's publishWithRetryAndBuffer (around the actual
			// PublishBatch call), not here — publish() is now async
			// (IMPROVEMENTS #1) and returns near-instantly, so
			// observing here would record enqueue time, not real
			// transport latency.
			//
			// totalPublish is also counted
			// inside the batcher via the onPublish callback (called when
			// the batch is actually published, not just enqueued). This
			// avoids inflating the publish counter for points that are
			// later lost after retries are exhausted (Finding 4 fix:
			// previously counted prematurely here).
			//
			// publish() returns an error only when the flush queue is
			// full and the batch is dropped — in that case the data is
			// lost, so we count the error and try the fallback transport.
			err := entry.batcher.publish(p.eng.runtime.ctx, point)
			if err != nil {
				slog.Error("batch dropped from flush queue", "target", entry.name, "error", err)
				p.eng.stats.totalErrors.Add(1)
				p.eng.transportMgr.tryFallback(point, entry.name, fallbackSnapshot)
			}
			continue
		}

		start := time.Now()
		err := entry.transport.Publish(p.eng.runtime.ctx, point)
		// Observe in both the global aggregate and the per-transport
		// labeled histograms (audit OBS-2).
		p.eng.stats.labeled.observePublish(entry.name, time.Since(start))
		p.eng.stats.labeled.observeAge(entry.name, time.Since(point.Timestamp))
		if err != nil {
			slog.Error("publish failed", "target", entry.name, "error", err)
			p.eng.stats.totalErrors.Add(1)
			p.eng.transportMgr.tryFallback(point, entry.name, fallbackSnapshot)
		} else {
			p.eng.stats.totalPublish.Add(1)
		}
	}
}

func (p *processingCore) fireAlert(point core.DataPoint, r core.Rule) {
	// Record the fire in the engine's first-class alert registry BEFORE
	// fan-out (audit OBS-4/HUB-6): the alert exists even if an
	// embedding host registered no OnAlert handler at all — the old
	// fire-and-forget path made alerts invisible in that case.
	if p.alerts != nil {
		p.alerts.record(r, point)
	}

	p.eng.mu.RLock()
	handlers := make([]func(core.DataPoint, core.Rule), len(p.alertHandlers))
	copy(handlers, p.alertHandlers)
	p.eng.mu.RUnlock()

	for _, h := range handlers {
		h(point, r)
	}
}

// --- Event Subscription ---

func (p *processingCore) Subscribe(filter string) (ch <-chan core.DataPoint, unsub func()) {
	// dataBus is only created in Start(); before Start it is nil.
	// Return a nil channel and no-op unsubscribe instead of panicking.
	if p.dataBus == nil {
		return nil, func() {}
	}
	return p.dataBus.Subscribe(filter)
}

// SubscribeWithBuffer is like Subscribe but lets the caller choose the
// subscriber channel buffer size.
func (p *processingCore) SubscribeWithBuffer(filter string, size int) (ch <-chan core.DataPoint, unsub func()) {
	// dataBus is only created in Start(); before Start it is nil.
	// Return a nil channel and no-op unsubscribe instead of panicking.
	if p.dataBus == nil {
		return nil, func() {}
	}
	return p.dataBus.SubscribeWithBuffer(filter, size)
}

func (p *processingCore) OnAlert(handler func(point core.DataPoint, rule core.Rule)) {
	p.eng.mu.Lock()
	p.alertHandlers = append(p.alertHandlers, handler)
	p.eng.mu.Unlock()
}

// --- CoreCEngine forwarders (core.EventSubscriber) ---

func (e *CoreCEngine) Subscribe(filter string) (ch <-chan core.DataPoint, unsub func()) {
	return e.processing.Subscribe(filter)
}

func (e *CoreCEngine) SubscribeWithBuffer(filter string, size int) (ch <-chan core.DataPoint, unsub func()) {
	return e.processing.SubscribeWithBuffer(filter, size)
}

func (e *CoreCEngine) OnAlert(handler func(point core.DataPoint, rule core.Rule)) {
	e.processing.OnAlert(handler)
}

// applyConfig covers the processing pipeline's tuning knobs. Zero/unset
// values keep the New() defaults (explicit-overrides-auto). B2, quality
// audit 2026-10-21: extracted from the engineRuntime dispatcher, which
// sat at the CI complexity cap (gocyclo 20) with one config branch left
// before failure.
func (p *processingCore) applyConfig(cfg *core.EngineConfig) {
	if cfg.DataBusSize > 0 {
		p.dataBusSize = cfg.DataBusSize
	}
	if cfg.Workers > 0 {
		p.numWorkers = cfg.Workers
	}
	if cfg.HighPriorityWorkers > 0 {
		p.numHighPriorityWorkers = cfg.HighPriorityWorkers
	}
	if d, err := time.ParseDuration(cfg.AlertActiveWindow); err == nil && d > 0 {
		p.alertActiveWindow = d
	}
}

// applyBadQualityPolicy parses and installs the on-bad-quality policy —
// the one engine config field that can fail (unknown policy name).
func (p *processingCore) applyBadQualityPolicy(name string) error {
	policy, err := parseBadQualityPolicy(name)
	if err != nil {
		return err
	}
	p.badQualityPolicy = policy
	return nil
}
