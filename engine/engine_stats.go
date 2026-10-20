package engine

import (
	"github.com/CoreC-Dev/CoreC/core"
)

func (e *CoreCEngine) Status() core.EngineStatus {
	return e.runtime.Status()
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

func (e *CoreCEngine) ReadLatencyHistogram() core.LatencySnapshot {
	return e.stats.labeled.globalReadSnapshot()
}

func (e *CoreCEngine) PublishLatencyHistogram() core.LatencySnapshot {
	return e.stats.labeled.globalPublishSnapshot()
}

func (e *CoreCEngine) DataAgeHistogram() core.LatencySnapshot {
	return e.stats.labeled.globalAgeSnapshot()
}

// ReadLatencyByDriver forwards to the stats collector for the metrics
// route's optional core.LabeledLatencyProvider role (audit OBS-2).
func (e *CoreCEngine) ReadLatencyByDriver() map[string]core.LatencySnapshot {
	return e.stats.labeled.readSnapshots()
}

// PublishLatencyByTransport forwards to the stats collector for the
// metrics route's optional core.LabeledLatencyProvider role.
func (e *CoreCEngine) PublishLatencyByTransport() map[string]core.LatencySnapshot {
	return e.stats.labeled.publishSnapshots()
}

// DataAgeByTransport forwards to the stats collector for the metrics
// route's optional core.LabeledLatencyProvider role.
func (e *CoreCEngine) DataAgeByTransport() map[string]core.LatencySnapshot {
	return e.stats.labeled.ageSnapshots()
}

// SchedulerHealth forwards to the scheduler for the metrics route's
// optional core.SchedulerHealthProvider role. The nil-receiver guard
// lives inside scheduler.Health so this stays a pure forwarder even
// before Start().
func (e *CoreCEngine) SchedulerHealth() []core.TaskHealth {
	return e.lifecycle.scheduler.Health()
}

// DeadLetterStats forwards to the write manager for the metrics
// route's optional core.DeadLetterStatsProvider role (queue depth +
// lifetime pushes).
func (e *CoreCEngine) DeadLetterStats() (size int, pushed uint64) {
	return e.writes.DeadLetterStats()
}

// IngressDeadLetters forwards to the transport manager for the
// /write/failed?source=ingress view (optional
// core.IngressDeadLetterProvider role).
func (e *CoreCEngine) IngressDeadLetters() []core.DeadLetterEntry {
	return e.transportMgr.ingressDeadLetters()
}

// ApplyDerivedTags forwards to the lifecycle role for hot-reloading the
// derived tag set (optional core.DerivedTagApplier role, audit CFG-1).
func (e *CoreCEngine) ApplyDerivedTags(configs []core.DerivedTagConfig) error {
	return e.lifecycle.applyDerivedTags(configs)
}

// --- Alert registry (audit OBS-4/HUB-6) ---

// ActiveAlerts forwards to the alert registry for GET /alerts
// (optional core.AlertProvider role).
func (e *CoreCEngine) ActiveAlerts() []core.Alert {
	return e.processing.alerts.activeAlerts()
}

// AlertHistory forwards to the alert registry for GET /alerts
// (optional core.AlertProvider role).
func (e *CoreCEngine) AlertHistory() []core.AlertEvent {
	return e.processing.alerts.alertHistory()
}

// AckAlert acknowledges the active alert with the given key
// (optional core.AlertProvider role).
func (e *CoreCEngine) AckAlert(key string) error {
	return e.processing.alerts.ack(key)
}

// SubscribeAlerts forwards to the alert registry for WS /alerts/stream
// (optional core.AlertStreamer role).
func (e *CoreCEngine) SubscribeAlerts() (<-chan core.AlertEvent, func()) {
	return e.processing.alerts.subscribe()
}

// AlertFireCounts forwards to the alert registry for
// corec_alerts_total{rule} (optional core.AlertMetricsProvider role).
func (e *CoreCEngine) AlertFireCounts() map[string]uint64 {
	return e.processing.alerts.fireCounts()
}

// ActiveAlertCount forwards to the alert registry for
// corec_alerts_active (optional core.AlertMetricsProvider role).
func (e *CoreCEngine) ActiveAlertCount() int {
	return e.processing.alerts.activeCount()
}

// --- Driver pause/resume (audit RES-4, roadmap #21) ---

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
