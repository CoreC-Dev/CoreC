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
	if point.Quality == core.QualityBad {
		switch e.badQualityPolicy {
		case badQualityDrop:
			e.totalErrors.Add(1)
			e.statManager.PushError()
			return // discard entirely
		case badQualityMarkAndPublish:
			point.Value = nil // keep quality=bad, clear value
		case badQualityAlert:
			// Forward + alert — handled below via fireAlert
		}
	}

	// Update cache
	e.cache.Update(point)

	// Broadcast to subscribers
	e.dataBus.Broadcast(point)

	// Match rules
	result := e.ruleEngine.Match(point)
	if result == nil {
		return
	}

	// Execute action
	alerted := false
	switch result.Rule.Action() {
	case core.ActionDrop:
		return
	case core.ActionAlert:
		e.fireAlert(point, result.Rule)
		alerted = true
		e.publishToTargets(point, result.Targets)
	case core.ActionTransform:
		e.publishToTargets(e.applyTransform(point, result.Transform), result.Targets)
	case core.ActionForward, core.ActionMirror:
		e.publishToTargets(point, result.Targets)
	}

	// Bad-quality alert: fire alert in addition to the normal action,
	// but skip if the rule action already fired an alert above.
	if point.Quality == core.QualityBad && e.badQualityPolicy == badQualityAlert && !alerted {
		e.fireAlert(point, result.Rule)
	}
}
