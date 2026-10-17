package engine

import (
	"github.com/CoreC-Dev/CoreC/common/metrics"
	"github.com/CoreC-Dev/CoreC/core"
)

func (e *CoreCEngine) Status() core.EngineStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status
}

// --- Rule Management ---

func (e *CoreCEngine) SetRules(rules []core.RuleConfig) error {
	return e.ruleSet.SetRules(rules)
}

func (e *CoreCEngine) GetRuleStats() []core.RuleStat {
	return e.ruleSet.GetRuleStats()
}

func (e *CoreCEngine) SetRuleDisabled(index int, disabled bool) error {
	return e.ruleSet.SetRuleDisabled(index, disabled)
}

func (e *CoreCEngine) SetRuleDisabledByName(name string, disabled bool) error {
	return e.ruleSet.SetRuleDisabledByName(name, disabled)
}

// Compile-time guard: the engine satisfies the optional rule-preview role
// that the route layer type-asserts.
var _ core.RulePreviewer = (*CoreCEngine)(nil)

func (e *CoreCEngine) PreviewRule(match string, point core.DataPoint) (bool, error) {
	return e.ruleSet.PreviewRule(match, point)
}

// --- Stats ---

func (e *CoreCEngine) Stats() core.EngineStats {
	return e.stats.Stats()
}

func (e *CoreCEngine) OfflineBufferStats() (pending int, drained, pushed uint64) {
	return e.stats.OfflineBufferStats()
}

func (e *CoreCEngine) OfflineBufferEvictions() uint64 {
	return e.stats.OfflineBufferEvictions()
}

func (e *CoreCEngine) FlushBatchesDropped() uint64 {
	return e.stats.FlushBatchesDropped()
}

func (e *CoreCEngine) ReadLatency() *metrics.LatencyHistogram {
	return e.stats.ReadLatency()
}

func (e *CoreCEngine) ReadLatencyHistogram() core.LatencySnapshot {
	return e.stats.ReadLatencyHistogram()
}

func (e *CoreCEngine) PublishLatency() *metrics.LatencyHistogram {
	return e.stats.PublishLatency()
}

func (e *CoreCEngine) PublishLatencyHistogram() core.LatencySnapshot {
	return e.stats.PublishLatencyHistogram()
}

func (e *CoreCEngine) DataAgeHistogram() core.LatencySnapshot {
	return e.stats.DataAgeHistogram()
}
