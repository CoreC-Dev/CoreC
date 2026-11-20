package core

import (
	"context"
	"time"
)

// Engine is the core orchestrator of the CoreC core, composed of the
// single-role interfaces below; implementers may depend on just the
// roles they need.
type Engine interface {
	Lifecycler
	DriverManager
	TransportManager
	RuleManager
	DataAccessor
	EventSubscriber
	StatsProvider
}

// Lifecycler covers engine start/stop and runtime state transitions.
type Lifecycler interface {
	Start(ctx context.Context, config *Config) error
	Stop() error
	Reload(config *Config) error
	Suspend() error
	Resume() error
	Status() EngineStatus
}

// DriverManager covers adding, removing, and inspecting drivers.
type DriverManager interface {
	AddDriver(config DriverConfig) error
	RemoveDriver(name string) error
	GetDriver(name string) (Driver, bool)
	ListDrivers() []DriverStatus
}

// TransportManager covers adding, removing, and inspecting transports.
type TransportManager interface {
	AddTransport(config TransportConfig) error
	RemoveTransport(name string) error
	GetTransport(name string) (Transport, bool)
	ListTransports() []TransportStatus
}

// RuleManager covers configuring rules and reading rule statistics.
type RuleManager interface {
	SetRules(rules []RuleConfig) error
	GetRuleStats() []RuleStat
	SetRuleDisabled(index int, disabled bool) error
	// SetRuleDisabledByName enables or disables a rule by its configured
	// name, a stable identifier across config reloads (names are unique,
	// enforced by config validation).
	SetRuleDisabledByName(name string, disabled bool) error
}

// RulePreviewer backs POST /rules/preview (offline rule trial). Optional
// role, type-asserted by the route layer; not part of Engine.
// *CoreCEngine satisfies it.
type RulePreviewer interface {
	// PreviewRule compiles match, evaluates it against point, and reports
	// whether a rule with that expression would fire on it. Compile
	// errors are returned verbatim.
	PreviewRule(match string, point DataPoint) (bool, error)
}

// DataAccessor covers synchronous read/write operations and cached
// latest values, used by the API layer.
type DataAccessor interface {
	ReadTag(ctx context.Context, driver, tag string) (*TagValue, error)
	WriteTag(ctx context.Context, cmd WriteCommand) (*WriteResult, error)
	LatestValues(driver string) map[string]DataPoint
}

// WriteAuditor exposes the bounded write audit history (successes and
// failures alike, newest first), backing GET /write/history. Optional
// role, type-asserted by the route layer; not part of Engine.
// *CoreCEngine satisfies it.
type WriteAuditor interface {
	// WriteHistory returns a newest-first copy of the audit history
	// (index 0 = most recent write attempt). The returned slice is a
	// copy; callers may retain and mutate it freely.
	WriteHistory() []WriteAuditEntry
}

// EventSubscriber covers real-time data subscriptions and alert handlers.
type EventSubscriber interface {
	Subscribe(filter string) (<-chan DataPoint, func())
	OnAlert(handler func(point DataPoint, rule Rule))
}

// StatsProvider covers aggregate engine statistics and staleness/dead-letter
// inspection. StaleThreshold returns the configured staleness threshold for
// cached values (0 if disabled); DeadLetterEntries returns write commands
// that failed after all retries, stored for inspection or manual retry.
type StatsProvider interface {
	Stats() EngineStats
	StaleThreshold() time.Duration
	DeadLetterEntries() []DeadLetterEntry
}

// LatencyProvider exposes read/publish latency histograms for Prometheus
// exposure. Optional role, type-asserted by the route layer; not part of
// Engine. *CoreCEngine satisfies it.
type LatencyProvider interface {
	ReadLatencyHistogram() LatencySnapshot
	PublishLatencyHistogram() LatencySnapshot
}

// LabeledLatencyProvider exposes the per-driver / per-transport latency
// histograms for Prometheus exposure. Optional role, type-asserted by
// the route layer; engines that do not implement it keep the unlabeled
// families. *CoreCEngine satisfies it.
type LabeledLatencyProvider interface {
	// ReadLatencyByDriver returns one read-latency snapshot per driver
	// that has completed at least one read.
	ReadLatencyByDriver() map[string]LatencySnapshot
	// PublishLatencyByTransport returns one publish-latency snapshot
	// per transport that has published at least once.
	PublishLatencyByTransport() map[string]LatencySnapshot
	// DataAgeByTransport returns one data-age snapshot per transport
	// that has published at least once.
	DataAgeByTransport() map[string]LatencySnapshot
}

// LatencySnapshot is a point-in-time copy of a latency histogram's state.
// Buckets and Counts are aligned: Buckets[i] is the upper bound (in seconds)
// and Counts[i] is the cumulative number of observations with duration <=
// Buckets[i]. The final +Inf bucket is implied by Count (total observations).
type LatencySnapshot struct {
	Buckets []float64
	Counts  []uint64
	Sum     float64
	Count   uint64
}

// OfflineBufferStatsProvider exposes offline-buffer observability counters
// for Prometheus exposure. Optional role, type-asserted by the route layer;
// not part of Engine. *CoreCEngine satisfies it.
type OfflineBufferStatsProvider interface {
	OfflineBufferStats() (pending int, drained uint64, pushed uint64)
}

// DataAgeProvider exposes the data-age (freshness) histogram for
// Prometheus exposure: elapsed time between a data point's collection
// timestamp (DataPoint.Timestamp) and the engine's publish attempt.
// Optional role, type-asserted by the route layer; not part of Engine.
// *CoreCEngine satisfies it.
type DataAgeProvider interface {
	DataAgeHistogram() LatencySnapshot
}

// FlushBatchesDroppedProvider exposes the total number of full batches
// evicted or dropped from batcher flush queues under sustained
// backpressure. Optional role, type-asserted by the route layer; not
// part of Engine. *CoreCEngine satisfies it.
type FlushBatchesDroppedProvider interface {
	FlushBatchesDropped() uint64
}

// DroppedBreakdown decomposes the dropped-point counters by cause.
type DroppedBreakdown struct {
	// DataBusMain is the number of points dropped from the main data
	// bus channel when it was full (oldest evicted).
	DataBusMain uint64 `json:"databus_main"`
	// DataBusPriority is the same for the high-priority (fast-interval)
	// channel.
	DataBusPriority uint64 `json:"databus_priority"`
	// SlowSubscriber is the number of points dropped because a
	// /tags/stream subscriber fell behind.
	SlowSubscriber uint64 `json:"slow_subscriber"`
	// BadQuality is the number of points discarded by the
	// on-bad-quality: drop policy.
	BadQuality uint64 `json:"bad_quality"`
	// LogEvents is the number of log events dropped by a full log
	// buffer or slow log subscribers (also exposed as
	// corec_log_dropped_total).
	LogEvents uint64 `json:"log_events"`
}

// DroppedBreakdownProvider exposes the per-cause drop counters for
// Prometheus exposure. Optional role, type-asserted by the route layer;
// not part of Engine. *CoreCEngine satisfies it.
type DroppedBreakdownProvider interface {
	DroppedBreakdown() DroppedBreakdown
}

// DeadLetterStatsProvider exposes the dead-letter queue depth (gauge)
// and the lifetime number of commands pushed into it (counter) for
// Prometheus exposure. Optional role, type-asserted by the route layer;
// not part of Engine. *CoreCEngine satisfies it.
type DeadLetterStatsProvider interface {
	DeadLetterStats() (size int, pushed uint64)
}

// IngressDeadLetterProvider exposes commands dropped at transport
// ingress (e.g. an MQTT command arriving while the command channel was
// full), diverted to the transport-local dead-letter store. Backs
// GET /write/failed?source=ingress. Optional role, type-asserted by the
// route layer; not part of Engine. *CoreCEngine satisfies it.
type IngressDeadLetterProvider interface {
	IngressDeadLetters() []DeadLetterEntry
}

// StaleThresholdPatcher applies global.engine.stale-threshold at runtime
// via PATCH /configs; the threshold gates /tags is_stale annotation.
// Optional role, type-asserted by the route layer; engines that do not
// implement it reject the patch key.
type StaleThresholdPatcher interface {
	SetStaleThreshold(d time.Duration)
}

// DerivedTagApplier hot-reloads the derived tag set. The executor
// type-asserts this optional role during config reload; engines that do
// not implement it keep the restart-required warning. *CoreCEngine
// satisfies it.
type DerivedTagApplier interface {
	ApplyDerivedTags(configs []DerivedTagConfig) error
}

// Config is the top-level configuration structure.
type Config struct {
	Node          NodeConfig              `yaml:"node,omitempty"`
	Global        GlobalConfig            `yaml:"global"`
	Drivers       []DriverConfig          `yaml:"drivers"`
	Transports    []TransportConfig       `yaml:"transports"`
	Rules         []RuleConfig            `yaml:"rules"`
	RuleProviders []RuleProviderConfig    `yaml:"rule-providers,omitempty"`
	RuleGroups    map[string][]RuleConfig `yaml:"rule-groups,omitempty"`
	DerivedTags   []DerivedTagConfig      `yaml:"derived-tags,omitempty"`
}

// NodeConfig holds node-level configuration for topology auto-discovery.
// When ID is non-empty, the engine enables auto-discovery: topic-template,
// command-topic, and parser are auto-generated where not explicitly set,
// and the node broadcasts heartbeats so other instances can find it.
// When ID is empty (the default), all auto-discovery features are
// disabled.
type NodeConfig struct {
	// ID is the unique identifier for this node in the topology.
	// Must be set to enable auto-discovery.
	ID string `yaml:"id"`

	// Role declares this node's function in the topology.
	//   collector  — has drivers, publishes data, no upstream
	//   relay      — receives from upstream, processes, republishes
	//   aggregator — receives from multiple upstreams, merges
	//   sink       — receives from upstream, does not republish
	Role string `yaml:"role"`

	// Subscribe lists upstream node IDs to receive data from.
	// The discovery module auto-subscribes to each upstream's publish
	// topic when the upstream comes online.
	Subscribe []string `yaml:"subscribe,omitempty"`

	// TopicPrefix overrides the default "topo" prefix for auto-generated
	// topics. Auto-generated format: {prefix}/{node-id}/data/{driver}/{tag}
	TopicPrefix string `yaml:"topic-prefix,omitempty"`
}

// GlobalConfig holds global settings.
type GlobalConfig struct {
	LogLevel  string       `yaml:"log-level"`
	LogFormat string       `yaml:"log-format"`
	API       APIConfig    `yaml:"api"`
	Engine    EngineConfig `yaml:"engine"`
	Buffer    BufferConfig `yaml:"buffer"`
}

// EngineConfig holds engine-wide runtime tuning parameters.
// All fields are optional; zero values fall back to sensible defaults.
type EngineConfig struct {
	// DataBusSize is the buffer capacity of the internal data channel
	// connecting drivers to the processing pipeline.
	// Default: 8192.
	DataBusSize int `yaml:"data-bus-size,omitempty"`

	// Workers is the number of processing goroutines for the rule
	// pipeline. 0 means runtime.NumCPU().
	Workers int `yaml:"workers,omitempty"`

	// ShutdownTimeout is the maximum time to wait for drivers and
	// transports to stop during graceful shutdown.
	// Default: 30s. Parse as a duration string, e.g. "30s".
	ShutdownTimeout string `yaml:"shutdown-timeout,omitempty"`

	// ErrorThrottleWindow is the time window within which repeated
	// error/overrun log messages from the scheduler are suppressed.
	// Default: 10s. Parse as a duration string, e.g. "10s".
	ErrorThrottleWindow string `yaml:"error-throttle-window,omitempty"`

	// DefaultTagInterval is the fallback collection interval for tags
	// that do not specify their own interval.
	// Default: 1s. Parse as a duration string, e.g. "1s".
	DefaultTagInterval string `yaml:"default-tag-interval,omitempty"`

	// OnBadQuality controls how DataPoints with QualityBad are handled
	// in the processing pipeline. Valid values:
	//   "publish"         — forward normally (default).
	//   "drop"            — discard bad-quality points before rule matching.
	//   "mark-and-publish" — publish but set Value to nil, keeping quality=bad.
	//   "alert"           — forward and trigger alert callback.
	// An empty value defaults to "publish". An unrecognized value returns
	// a startup error.
	OnBadQuality string `yaml:"on-bad-quality,omitempty"`

	// StaleThreshold is the duration after which a cached data point is
	// considered stale (not updated within this window). The API layer
	// uses this to annotate /tags and /drivers/{name}/tags responses
	// with an is_stale flag so consumers can distinguish live data
	// from values held over from a disconnected driver.
	// Default: 0 (disabled; no staleness annotation). e.g. "30s".
	StaleThreshold string `yaml:"stale-threshold,omitempty"`

	// WriteRetryCount is the number of times to retry a failed write
	// command before placing it in the dead letter queue.
	// Unset or 0 uses the default (3 retries). Set to a positive value
	// to override. The total attempt count is WriteRetryCount + 1.
	WriteRetryCount int `yaml:"write-retry-count,omitempty"`

	// CommandConcurrency is the maximum number of write commands the
	// engine executes in parallel. Commands in excess of this limit
	// apply backpressure (the transport's command channel fills, and
	// overflow is diverted to the dead-letter store).
	// Unset or 0 uses the default (16); 1 executes writes serially.
	CommandConcurrency int `yaml:"command-concurrency,omitempty"`

	// HighPriorityWorkers is the number of dedicated processing workers
	// for high-priority (fast-interval) data. These workers read only
	// from the DataBus high-priority channel, so a burst of low-frequency
	// bulk reads cannot starve high-frequency collection. Unset or 0 uses
	// the default (2).
	HighPriorityWorkers int `yaml:"high-priority-workers,omitempty"`

	// DeadLetterPath enables on-disk persistence of the write dead-letter
	// queue: an append-only JSONL file that survives crashes and replays
	// into the queue at startup. Empty = disabled (the queue stays
	// memory-only). The file rotates to
	// <path>.old past 5 MiB, bounding disk at ~2×5 MiB.
	DeadLetterPath string `yaml:"dead-letter-path,omitempty"`

	// WriteMinInterval is the minimum spacing between two accepted writes
	// to the same driver. A write arriving inside the window is refused:
	// the API answers 429 with a Retry-After header; the command channel
	// waits out the remaining window and retries (pacing retries do not
	// consume normal retries), and dead-letters the command with the
	// reason intact once those retries are exhausted. Only accepted writes
	// advance the window. Empty = disabled (writes proceed immediately).
	// Parse as a duration string, e.g. "200ms".
	WriteMinInterval string `yaml:"write-min-interval,omitempty"`

	// WriteAuditPath enables on-disk persistence of the write audit trail
	// (GET /write/history): an append-only JSONL file that survives
	// process restarts. Empty = disabled (in-memory only). Records are
	// fsynced before the write returns; the file rotates to <path>.old
	// past 5 MiB (bounding disk at ~2x5 MiB) and the newest entries
	// replay into the in-memory ring at startup.
	WriteAuditPath string `yaml:"write-audit-path,omitempty"`

	// AlertActiveWindow is how long an alert stays active after its
	// last fire. An alert that has not re-fired for a full window is
	// cleared (moves to history and stops appearing in GET /alerts).
	// Acknowledgement does not clear an alert — only silence does.
	// Default: 15m. Parse as a duration string, e.g. "15m".
	AlertActiveWindow string `yaml:"alert-active-window,omitempty"`
}

// APIConfig holds API server settings.
type APIConfig struct {
	Listen          string   `yaml:"listen"`
	Secret          string   `yaml:"secret"`
	TLSCert         string   `yaml:"tls-cert,omitempty"`
	TLSKey          string   `yaml:"tls-key,omitempty"`
	AllowedOrigins  []string `yaml:"allowed-origins,omitempty"`
	RateLimitPerSec int      `yaml:"rate-limit-per-sec,omitempty"`

	// ReadHeaderTimeout is the maximum duration for reading the request
	// headers. Default: 10s. Parse as a duration string.
	ReadHeaderTimeout string `yaml:"read-header-timeout,omitempty"`

	// ReadTimeout is the maximum duration for reading the entire request.
	// Default: 0 (disabled; no overall read deadline). Parse as a duration string.
	ReadTimeout string `yaml:"read-timeout,omitempty"`

	// WriteTimeout is the maximum duration before timing out writes of the
	// response. Default: 0 (disabled; no overall write deadline). Parse as a duration string.
	WriteTimeout string `yaml:"write-timeout,omitempty"`

	// IdleTimeout is the maximum amount of time to wait for the next
	// request when keep-alives are enabled. Default: 120s.
	IdleTimeout string `yaml:"idle-timeout,omitempty"`

	// PprofDisabled controls whether pprof profiling endpoints are
	// disabled. Defaults to false (pprof enabled).
	PprofDisabled bool `yaml:"pprof-disabled,omitempty"`

	// MetricsAddr, when non-empty, serves GET /metrics unauthenticated on
	// a separate HTTP server; only loopback addresses (127.0.0.1/::1/
	// localhost) are accepted — any other address refuses startup
	// (fail-closed, like PprofAddr). While set, /metrics is removed from
	// the main authenticated API. Empty = metrics stay behind the main
	// API's bearer auth.
	MetricsAddr string `yaml:"metrics-addr,omitempty"`

	// PprofAddr, when non-empty, starts a separate unauthenticated HTTP
	// server for pprof on this address (e.g. "127.0.0.1:6060"); only
	// loopback addresses (127.0.0.1/::1/localhost) are accepted — any
	// other address refuses startup (fail-closed). When empty, pprof runs
	// on the main API port (unless PprofDisabled is true).
	PprofAddr string `yaml:"pprof-addr,omitempty"`
}

// BufferConfig holds offline buffer settings for persisting failed
// publish batches to disk so they can be replayed after transport
// recovery. This prevents data loss when the northbound transport
// (MQTT broker / HTTP endpoint) is temporarily unavailable.
type BufferConfig struct {
	Enabled bool   `yaml:"enabled"`
	MaxSize int    `yaml:"max-size"` // max number of buffered batches; <=0 defaults to 10000
	Path    string `yaml:"path"`     // directory path for buffer files
}

// EngineStatus represents the operational state of the engine.
type EngineStatus string

const (
	EngineStatusRunning   EngineStatus = "running"
	EngineStatusSuspended EngineStatus = "suspended"
	EngineStatusStopped   EngineStatus = "stopped"
)

// EngineStats contains runtime statistics.
type EngineStats struct {
	Status         EngineStatus               `json:"status"`
	Uptime         time.Duration              `json:"uptime"`
	Drivers        int                        `json:"drivers"`
	Transports     int                        `json:"transports"`
	Rules          int                        `json:"rules"`
	TotalRead      uint64                     `json:"total_read"`
	TotalPublish   uint64                     `json:"total_publish"`
	TotalErrors    uint64                     `json:"total_errors"`
	TotalDropped   uint64                     `json:"total_dropped"`
	PointsPerSec   float64                    `json:"points_per_sec"`
	DriverStats    map[string]DriverStatus    `json:"driver_stats"`
	TransportStats map[string]TransportStatus `json:"transport_stats"`
}
