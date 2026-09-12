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
}
