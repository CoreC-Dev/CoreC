package core

import (
	"encoding/json"
	"fmt"
	"time"
)

// DataType represents the data type of a tag value.
type DataType int

const (
	TypeBool DataType = iota
	TypeInt8
	TypeInt16
	TypeInt32
	TypeInt64
	TypeUint8
	TypeUint16
	TypeUint32
	TypeUint64
	TypeFloat32
	TypeFloat64
	TypeString
	TypeBytes
)

var dataTypeNames = map[DataType]string{
	TypeBool: "bool", TypeInt8: "int8", TypeInt16: "int16", TypeInt32: "int32",
	TypeInt64: "int64", TypeUint8: "uint8", TypeUint16: "uint16", TypeUint32: "uint32",
	TypeUint64: "uint64", TypeFloat32: "float32", TypeFloat64: "float64",
	TypeString: "string", TypeBytes: "bytes",
}

var dataTypeFromString = map[string]DataType{
	"bool": TypeBool, "int8": TypeInt8, "int16": TypeInt16, "int32": TypeInt32,
	"int64": TypeInt64, "uint8": TypeUint8, "uint16": TypeUint16, "uint32": TypeUint32,
	"uint64": TypeUint64, "float32": TypeFloat32, "float64": TypeFloat64,
	"string": TypeString, "bytes": TypeBytes,
}

func (d DataType) String() string {
	if s, ok := dataTypeNames[d]; ok {
		return s
	}
	return "unknown"
}

// MarshalJSON serializes DataType as a human-readable string (e.g. "float32")
// rather than a raw integer. This makes JSON payloads in published data points
// and the /tags API self-describing, and matches the string form used in YAML
// tag configurations (type: float32).
func (d DataType) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts both the string form ("float32") and the legacy integer
// form (9). The string form is preferred for new integrations because it is
// self-documenting; the integer form is accepted for backward compatibility
// with existing clients and persisted payloads.
//
// This dual acceptance resolves a common usability trap: tag configurations in
// YAML use string types (type: float32), but prior to this change, the JSON API
// and MQTT command payloads required the raw iota integer (type: 9). Now both
// forms work in every JSON context.
func (d *DataType) UnmarshalJSON(data []byte) error {
	// Try string form first: "float32".
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		parsed, ok := ParseDataType(s)
		if !ok {
			return fmt.Errorf("invalid data type %q: expected one of %s", s, validDataTypeNames())
		}
		*d = parsed
		return nil
	}
	// Fall back to legacy integer form: 9.
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("data type must be a string (e.g. \"float32\") or integer, got %s", string(data))
	}
	parsed := DataType(n)
	if _, ok := dataTypeNames[parsed]; !ok {
		return fmt.Errorf("invalid data type integer %d: expected 0-%d", n, int(TypeBytes))
	}
	*d = parsed
	return nil
}

// validDataTypeNames returns the accepted string names for use in error messages.
func validDataTypeNames() string {
	return "bool, int8, int16, int32, int64, uint8, uint16, uint32, uint64, float32, float64, string, bytes"
}

// ParseDataType parses a string into a DataType.
func ParseDataType(s string) (DataType, bool) {
	d, ok := dataTypeFromString[s]
	return d, ok
}

// Quality represents the quality of a data value (inspired by OPC UA).
type Quality int

const (
	QualityGood      Quality = 0
	QualityBad       Quality = 1
	QualityUncertain Quality = 2
)

func (q Quality) String() string {
	switch q {
	case QualityGood:
		return "good"
	case QualityBad:
		return "bad"
	case QualityUncertain:
		return "uncertain"
	default:
		return "unknown"
	}
}

// ConnState represents the connection state of a driver or transport.
type ConnState int

const (
	StateDisconnected ConnState = iota
	StateConnecting
	StateConnected
	StateError
)

func (s ConnState) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// Action defines the action to take when a rule matches.
type Action int

const (
	ActionForward   Action = iota // Forward to target transport
	ActionDrop                    // Discard the data point
	ActionAlert                   // Forward + trigger alert callback
	ActionTransform               // Transform value then forward
	ActionMirror                  // Forward to multiple targets
)

// TagValue represents a raw value read from a device.
type TagValue struct {
	Tag       string    `json:"tag"`
	Value     any       `json:"value"`
	Type      DataType  `json:"type"`
	Quality   Quality   `json:"quality"`
	Timestamp time.Time `json:"timestamp"`
	Error     error     `json:"-"`
}

// DataPoint is the standard data unit flowing through the engine.
type DataPoint struct {
	Driver    string            `json:"driver"`
	Device    string            `json:"device"`
	Group     string            `json:"group"`
	Tag       string            `json:"tag"`
	Value     any               `json:"value"`
	Type      DataType          `json:"type"`
	Quality   Quality           `json:"quality"`
	Timestamp time.Time         `json:"timestamp"`
	Metadata  map[string]string `json:"metadata,omitempty"`

	// IsStale is set by the API layer when serving cached values that
	// haven't been updated within the configured stale-threshold. It is
	// not set during normal pipeline processing, so it does not appear
	// in published payloads (omitempty).
	IsStale bool `json:"is_stale,omitempty"`
}

// WriteCommand represents a command to write a value to a device.
type WriteCommand struct {
	Driver string   `json:"driver"`
	Device string   `json:"device"`
	Tag    string   `json:"tag"`
	Value  any      `json:"value"`
	Type   DataType `json:"type"`
}

// WriteResult represents the result of a write operation.
type WriteResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// DeadLetterEntry represents a failed write command stored in the dead
// letter queue for later inspection or manual retry.
type DeadLetterEntry struct {
	Command  WriteCommand `json:"command"`
	Error    string       `json:"error"`
	FailedAt time.Time    `json:"failed_at"`
	Attempts int          `json:"attempts"`
}

// TagConfig defines the configuration for a single tag/data point.
type TagConfig struct {
	Name     string  `yaml:"name"`
	Address  string  `yaml:"address"`
	Type     string  `yaml:"type"`
	Group    string  `yaml:"group"`
	Interval string  `yaml:"interval"`
	Scale    float64 `yaml:"scale,omitempty"`
	Offset   float64 `yaml:"offset,omitempty"`
	DeadBand float64 `yaml:"deadband,omitempty"`

	// ReadTimeout is an optional per-tag read timeout, independent of the
	// collection interval. When unset (empty), the scheduler falls back to
	// the collection interval. This decouples "how often to poll" from
	// "how long to wait for a response", which matters for high-frequency
	// 采集 (e.g. interval=200ms but read-timeout=1s) and low-frequency
	// 采集 (e.g. interval=10s but read-timeout=3s).
	ReadTimeout string `yaml:"read-timeout,omitempty"`
}

// ─── Shared default constants ───────────────────────────────────────
//
// These constants centralise tunable defaults that were previously
// duplicated as magic numbers across drivers, transports, and the
// engine.  Code should reference these instead of writing literal
// values, so that a future change only needs one edit.

const (
	// DefaultTagInterval is the fallback collection interval for tags
	// that do not specify their own interval.
	DefaultTagInterval = time.Second

	// DefaultDataBusSize is the default buffer capacity for the internal
	// data channel connecting drivers to the processing pipeline.
	DefaultDataBusSize = 8192

	// DefaultShutdownTimeout is the default maximum time to wait for
	// drivers and transports to stop during graceful shutdown.
	DefaultShutdownTimeout = 30 * time.Second

	// DefaultErrorThrottleWindow is the default time window for
	// suppressing repeated error/overrun log messages in the scheduler.
	DefaultErrorThrottleWindow = 10 * time.Second

	// DefaultReconnectBackoff is the initial delay before the first
	// reconnect attempt after a connection loss.
	DefaultReconnectBackoff = 2 * time.Second

	// DefaultMaxReconnectBackoff is the upper bound for the exponential
	// reconnect backoff delay.
	DefaultMaxReconnectBackoff = 30 * time.Second

	// DefaultCommandBufferSize is the default buffer size for transport
	// command channels when TransportConfig.BufferSize is not set.
	DefaultCommandBufferSize = 100

	// DefaultCommandConcurrency is the default maximum number of write
	// commands executed in parallel by the engine. Commands exceeding this
	// limit apply backpressure to the transport's command channel (and
	// ultimately to the dead-letter store per #4). This prevents a single
	// slow write from serializing all subsequent control commands
	// (IMPROVEMENTS #2).
	DefaultCommandConcurrency = 16

	// DefaultHighPriorityWorkers is the default number of dedicated
	// processing workers for high-priority (fast-interval) data. These
	// workers read only from the DataBus high-priority channel, so a burst
	// of low-frequency bulk reads cannot starve high-frequency collection
	// (IMPROVEMENTS #5).
	DefaultHighPriorityWorkers = 2

	// DefaultHighPriorityInterval is the collection interval threshold at
	// or below which a task is classified as high-priority. Tasks with
	// interval ≤ this value are routed to the dedicated high-priority
	// worker pool (IMPROVEMENTS #5).
	DefaultHighPriorityInterval = time.Second

	// DefaultBatchSize is the default batch size for transport batchers
	// when TransportConfig.BatchSize is not set.
	DefaultBatchSize = 100

	// DefaultMaxReconnectFailures is the circuit-breaker threshold: the
	// number of consecutive reconnect failures before the driver enters
	// a long cool-down period.
	DefaultMaxReconnectFailures = 20

	// DefaultDriverTimeout is the default I/O timeout for driver
	// connections (read/write operations).
	DefaultDriverTimeout = 5 * time.Second

	// DefaultIdleTimeout is the default connection idle timeout for
	// drivers that support keep-alive or idle detection.
	DefaultIdleTimeout = 60 * time.Second

	// DefaultTransportTimeout is the default I/O timeout for transport
	// publish/subscribe operations.
	DefaultTransportTimeout = 5 * time.Second

	// DefaultKeepAlive is the default keep-alive interval for transport
	// connections (e.g. MQTT ping interval).
	DefaultKeepAlive = 60 * time.Second

	// DefaultCircuitBreakerBackoff is the cool-down period after the
	// maximum number of reconnect failures is exceeded, before retrying.
	DefaultCircuitBreakerBackoff = 5 * time.Minute

	// DefaultOfflineBufferMaxEntries is the maximum number of data points
	// retained in the offline buffer when transport backends are unavailable.
	DefaultOfflineBufferMaxEntries = 10000

	// DefaultDeadLetterMaxLen is the maximum number of entries in the
	// engine's dead-letter queue for data points that could not be processed.
	DefaultDeadLetterMaxLen = 1000
)
