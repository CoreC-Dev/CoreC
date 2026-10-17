package engine

import (
	"github.com/CoreC-Dev/CoreC/core"
)

func (e *CoreCEngine) processingLoop() {
	defer e.wg.Done()

	for {
		select {
		case <-e.ctx.Done():
			return
		case point, ok := <-e.dataBus.Channel():
			if !ok {
				return
			}
			e.processPoint(point)
		}
	}
}

// processingLoopHighPriority is the dedicated worker for high-priority
// (fast-interval) data. It reads only from the DataBus high-priority
// channel, so a burst of low-frequency bulk reads on the main channel
// cannot delay high-frequency collection (IMPROVEMENTS #5).
func (e *CoreCEngine) processingLoopHighPriority() {
	defer e.wg.Done()

	for {
		select {
		case <-e.ctx.Done():
			return
		case point, ok := <-e.dataBus.HighPriorityChannel():
			if !ok {
				return
			}
			e.processPoint(point)
		}
	}
}

// processPoint applies the full processing pipeline to a single DataPoint:
// bad-quality policy, cache update, subscriber broadcast, rule matching,
// and action execution (publish/transform/alert/drop). It is shared by both
// the normal and high-priority processing loops.
func (e *CoreCEngine) processPoint(point core.DataPoint) {
	// Apply bad-quality policy before any further processing.
	// This prevents downstream systems from receiving
	// misleading bad-quality values unless explicitly configured.
	policyAlerted := false
	if point.Quality == core.QualityBad {
		switch e.badQualityPolicy {
		case badQualityDrop:
			e.totalErrors.Add(1)
			e.statManager.PushError()
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
			e.fireAlert(point, badQualityAlertRule{})
			policyAlerted = true
		}
	}

	// Update cache
	e.cache.Update(point)

	// Broadcast to subscribers
	e.dataBus.Broadcast(point)

	// Match rules — returns all matching rules in priority order, up to
	// and including the first terminal match (Continue() == false).
	// All rules are matched against the ORIGINAL point; transforms in the
	// action loop below mutate the point for publishing but do not affect
	// which rules matched (see ADR-010 § Limitations).
	results := e.ruleEngine.Match(point)
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
				e.fireAlert(point, result.Rule)
			}
			e.publishToTargets(pointWithRule(point, ruleName), result.Targets)
		case core.ActionTransform:
			// Transform mutates the point for subsequent action publishing:
			// a later rule's publishToTargets sees the transformed value.
			// However, the later rule's MATCH was already evaluated against
			// the original point — transforms do not re-trigger matching.
			point = e.applyTransform(point, result.Transform)
			e.publishToTargets(pointWithRule(point, ruleName), result.Targets)
		case core.ActionForward, core.ActionMirror:
			e.publishToTargets(pointWithRule(point, ruleName), result.Targets)
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
