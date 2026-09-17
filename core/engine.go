package core

import (
	"context"
	"time"
)

// Engine is the core orchestrator of the CoreC core.
//
// It is a composite interface assembled from single-role interfaces so
// that consumers can depend on exactly the capabilities they need
// (Interface Segregation Principle) instead of the full 25-method
// surface. Any type that implements every method below implements
// Engine, so this decomposition is fully backward compatible: existing
// implementations and mocks that embed Engine continue to satisfy the
// interface unchanged.
//
// The role interfaces are:
//   - Lifecycler        — Start, Stop, Reload, Suspend, Resume, Status
//   - DriverManager     — AddDriver, RemoveDriver, GetDriver, ListDrivers
//   - TransportManager  — AddTransport, RemoveTransport, GetTransport, ListTransports
//   - RuleManager       — SetRules, GetRuleStats, SetRuleDisabled
//   - DataAccessor      — ReadTag, WriteTag, LatestValues
//   - EventSubscriber   — Subscribe, OnAlert
//   - StatsProvider     — Stats, StaleThreshold, DeadLetterEntries
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
}

// DataAccessor covers synchronous read/write operations and cached
// latest values, used by the API layer.
type DataAccessor interface {
	ReadTag(ctx context.Context, driver, tag string) (*TagValue, error)
	WriteTag(ctx context.Context, cmd WriteCommand) (*WriteResult, error)
	LatestValues(driver string) map[string]DataPoint
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

// LatencyProvider exposes latency histograms for Prometheus histogram
// exposure. It is a separate role interface (not embedded in Engine) so
// that the route layer can type-assert optionally without coupling to the
// concrete engine type. *CoreCEngine satisfies this interface; test mocks
// are free to omit it.
type LatencyProvider interface {
	ReadLatencyHistogram() LatencySnapshot
	PublishLatencyHistogram() LatencySnapshot
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
// for Prometheus exposure. It is a separate role interface so the route
// layer can type-assert optionally. *CoreCEngine satisfies this interface.
type OfflineBufferStatsProvider interface {
	OfflineBufferStats() (pending int, drained uint64, pushed uint64)
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
}

// NodeConfig holds node-level configuration for topology auto-discovery.
// When ID is non-empty, the engine enables auto-discovery: topic-template,
// command-topic, and parser are auto-generated where not explicitly set,
// and the node broadcasts heartbeats so other instances can find it.
// When ID is empty (the default), all auto-discovery features are disabled
// and the engine behaves exactly as before — full backward compatibility.
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
	// connecting drivers to the processing pipeline. Larger values
	// reduce drops under burst load at the cost of memory.
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
	//   "publish"         — forward normally (default, backward-compatible).
	//   "drop"            — discard bad-quality points before rule matching.
	//   "mark-and-publish" — publish but set Value to nil, keeping quality=bad.
	//   "alert"           — forward and trigger alert callback.
	// An empty/unrecognized value defaults to "publish".
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
	// disabled. Defaults to false (pprof enabled) for backward
	// compatibility. Set to true to completely disable pprof.
	PprofDisabled bool `yaml:"pprof-disabled,omitempty"`

	// PprofAddr, when non-empty, starts a separate HTTP server for pprof
	// on this address (e.g. "127.0.0.1:6060"). The separate server does
	// NOT require authentication, so it should be bound to a loopback or
	// private interface only. When empty, pprof runs on the main API
	// port (unless PprofDisabled is true).
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
