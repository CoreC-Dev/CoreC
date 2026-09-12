package core

import (
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

	// DefaultBatchSize is the default batch size for transport batchers
	// when TransportConfig.BatchSize is not set.
	DefaultBatchSize = 100
)
