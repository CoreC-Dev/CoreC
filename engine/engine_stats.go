package engine

import (
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func (e *CoreCEngine) Status() core.EngineStatus {
	return e.runtime.Status()
}

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

func (e *CoreCEngine) Stats() core.EngineStats {
	return e.stats.Stats()
}

func (e *CoreCEngine) OfflineBufferStats() (pending int, drained, pushed uint64) {
	return e.stats.OfflineBufferStats()
}

func (e *CoreCEngine) OfflineBufferEvictions() uint64 {
	return e.stats.OfflineBufferEvictions()
}

func (e *CoreCEngine) OfflineBufferBytes() int64 {
	return e.stats.OfflineBufferBytes()
}

func (e *CoreCEngine) FlushBatchesDropped() uint64 {
	return e.stats.FlushBatchesDropped()
}

func (e *CoreCEngine) ReadLatencyHistogram() core.LatencySnapshot {
	return e.stats.labeled.globalReadSnapshot()
}

func (e *CoreCEngine) PublishLatencyHistogram() core.LatencySnapshot {
	return e.stats.labeled.globalPublishSnapshot()
}

func (e *CoreCEngine) DataAgeHistogram() core.LatencySnapshot {
	return e.stats.labeled.globalAgeSnapshot()
}

func (e *CoreCEngine) ReadLatencyByDriver() map[string]core.LatencySnapshot {
	return e.stats.labeled.readSnapshots()
}

func (e *CoreCEngine) PublishLatencyByTransport() map[string]core.LatencySnapshot {
	return e.stats.labeled.publishSnapshots()
}

func (e *CoreCEngine) DataAgeByTransport() map[string]core.LatencySnapshot {
	return e.stats.labeled.ageSnapshots()
}

func (e *CoreCEngine) SchedulerHealth() []core.TaskHealth {
	return e.lifecycle.scheduler.Health()
}

func (e *CoreCEngine) DeadLetterStats() (size int, pushed uint64) {
	return e.writes.DeadLetterStats()
}

func (e *CoreCEngine) IngressDeadLetters() []core.DeadLetterEntry {
	return e.transportMgr.ingressDeadLetters()
}

func (e *CoreCEngine) ApplyDerivedTags(configs []core.DerivedTagConfig) error {
	return e.lifecycle.applyDerivedTags(configs)
}

func (e *CoreCEngine) ActiveAlerts() []core.Alert {
	return e.processing.alerts.activeAlerts()
}

func (e *CoreCEngine) AlertHistory() []core.AlertEvent {
	return e.processing.alerts.alertHistory()
}

func (e *CoreCEngine) AckAlert(key string) error {
	return e.processing.alerts.ack(key)
}

func (e *CoreCEngine) SubscribeAlerts() (ch <-chan core.AlertEvent, unsub func()) {
	return e.processing.alerts.subscribe()
}

func (e *CoreCEngine) AlertFireCounts() map[string]uint64 {
	return e.processing.alerts.fireCounts()
}

func (e *CoreCEngine) ActiveAlertCount() int {
	return e.processing.alerts.activeCount()
}

func (e *CoreCEngine) DroppedBreakdown() core.DroppedBreakdown {
	return e.stats.DroppedBreakdown()
}

func (e *CoreCEngine) DriverMetrics() []core.DriverMetric {
	return e.stats.DriverMetrics()
}

func (e *CoreCEngine) SetStaleThreshold(d time.Duration) {
	e.reads.SetStaleThreshold(d)
}

// PauseDriver suspends the named driver's collection tasks without
// removing them (the scheduler flags them; ticks skip while paused).
// The route layer validates the driver exists first.
func (e *CoreCEngine) PauseDriver(name string) error {
	return e.lifecycle.scheduler.PauseDriver(name)
}

// ResumeDriver clears the pause flag; tasks resume at their next tick.
func (e *CoreCEngine) ResumeDriver(name string) error {
	return e.lifecycle.scheduler.ResumeDriver(name)
}

// PausedDrivers returns the sorted names of currently-paused drivers.
func (e *CoreCEngine) PausedDrivers() []string {
	return e.lifecycle.scheduler.PausedDrivers()
}
