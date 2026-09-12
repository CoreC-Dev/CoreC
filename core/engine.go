package core

import (
	"context"
	"time"
)

// Engine is the core orchestrator of the CoreC core.
type Engine interface {
	// Lifecycle
	Start(ctx context.Context, config *Config) error
	Stop() error
	Reload(config *Config) error
	Suspend() error
	Resume() error
	Status() EngineStatus

	// Driver management
	AddDriver(config DriverConfig) error
	RemoveDriver(name string) error
	GetDriver(name string) (Driver, bool)
	ListDrivers() []DriverStatus

	// Transport management
	AddTransport(config TransportConfig) error
	RemoveTransport(name string) error
	GetTransport(name string) (Transport, bool)
	ListTransports() []TransportStatus

	// Rule management
	SetRules(rules []RuleConfig) error
	GetRuleStats() []RuleStat
	SetRuleDisabled(index int, disabled bool) error

	// Data operations (for API layer)
	ReadTag(ctx context.Context, driver, tag string) (*TagValue, error)
	WriteTag(ctx context.Context, cmd WriteCommand) (*WriteResult, error)
	LatestValues(driver string) map[string]DataPoint

	// Event subscription
	Subscribe(filter string) (<-chan DataPoint, func())
	OnAlert(handler func(point DataPoint, rule Rule))

	// Status
	Stats() EngineStats
}

// Config is the top-level configuration structure.
type Config struct {
	Global        GlobalConfig            `yaml:"global"`
	Drivers       []DriverConfig          `yaml:"drivers"`
	Transports    []TransportConfig       `yaml:"transports"`
	Rules         []RuleConfig            `yaml:"rules"`
	RuleProviders []RuleProviderConfig    `yaml:"rule-providers,omitempty"`
	RuleGroups    map[string][]RuleConfig `yaml:"rule-groups,omitempty"`
}

// GlobalConfig holds global settings.
type GlobalConfig struct {
	LogLevel string       `yaml:"log-level"`
	API      APIConfig    `yaml:"api"`
	Engine   EngineConfig `yaml:"engine"`
	Buffer   BufferConfig `yaml:"buffer"`
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
	// Default: 30s. Parse as a duration string.
	ReadTimeout string `yaml:"read-timeout,omitempty"`

	// WriteTimeout is the maximum duration before timing out writes of the
	// response. Default: 30s. Parse as a duration string.
	WriteTimeout string `yaml:"write-timeout,omitempty"`

	// IdleTimeout is the maximum amount of time to wait for the next
	// request when keep-alives are enabled. Default: 120s.
	IdleTimeout string `yaml:"idle-timeout,omitempty"`
}

// BufferConfig holds offline buffer settings.
//
// Deprecated: buffer is parsed for backwards compatibility but not used.
// A non-empty buffer config will trigger a validation warning.
type BufferConfig struct {
	Enabled bool   `yaml:"enabled"`
	MaxSize int    `yaml:"max-size"`
	Path    string `yaml:"path"`
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
