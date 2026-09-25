package core

import (
	"context"
	"time"
)

// Transport defines the northbound transport interface.
type Transport interface {
	// Lifecycle
	Init(ctx context.Context, config TransportConfig) error
	Start(ctx context.Context) error
	Stop() error

	// Data publishing
	Publish(ctx context.Context, point DataPoint) error
	PublishBatch(ctx context.Context, points []DataPoint) error

	// Reverse channel for receiving write commands
	OnCommand() <-chan WriteCommand

	// OnData returns a channel of DataPoints received from an upstream
	// source (e.g. another CoreC instance via MQTT subscription or an
	// HTTP webhook).  This enables chained-core topologies where a
	// transport acts as both a data consumer (inbound) and a data
	// producer (outbound).  Returns nil when data ingestion is not
	// configured for this transport.
	OnData() <-chan DataPoint

	// Metadata
	Name() string
	Type() string
	Status() TransportStatus
}

// CommandForwarder is an optional capability for transports that can
// republish write commands to downstream nodes. This enables chained-core
// command passthrough: when a relay node receives a command via
// command-topic but has no local driver for the target, it forwards the
// command to downstream nodes via the transport's forward topic.
//
// Transports implement this interface to opt in to command forwarding.
// The engine type-asserts for this interface; transports that don't
// implement it are simply skipped during forwarding.
type CommandForwarder interface {
	ForwardCommand(ctx context.Context, cmd WriteCommand) error
}

// TransportConfig holds configuration for a transport instance.
type TransportConfig struct {
	Name          string         `yaml:"name"`
	Type          string         `yaml:"type"`
	Settings      map[string]any `yaml:"settings"`
	BatchSize     int            `yaml:"batch-size,omitempty"`
	FlushInterval string         `yaml:"flush-interval,omitempty"`
	RetryCount    int            `yaml:"retry-count,omitempty"`
	// BufferSize is the capacity of the internal command channel.
	// <=0 falls back to core.DefaultCommandBufferSize (100).
	BufferSize int `yaml:"buffer-size,omitempty"`
	// Fallback names a secondary transport to use when this transport
	// fails to publish. The engine routes the data point to the
	// fallback transport on publish failure. Empty = no fallback.
	Fallback string `yaml:"fallback,omitempty"`
}

// TransportStatus reports the current status of a transport.
type TransportStatus struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	State       ConnState `json:"state"`
	Published   uint64    `json:"published"`
	Failed      uint64    `json:"failed"`
	Received    uint64    `json:"received"` // data points ingested via OnData (chained-core inbound)
	LastPublish time.Time `json:"last_publish"`
	QueueSize   int       `json:"queue_size"`
	// DroppedCommands counts write commands dropped at ingress because the
	// command channel was full. Non-zero indicates sustained command
	// backpressure; investigate buffer-size tuning or consumer throughput.
	DroppedCommands uint64 `json:"dropped_commands"`
}
