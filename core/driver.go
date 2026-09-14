package core

import (
	"context"
	"errors"
	"time"
)

// ErrSubscribeNotSupported is returned by drivers that don't support subscription mode.
var ErrSubscribeNotSupported = errors.New("subscribe not supported by this driver")

// Driver defines the southbound driver interface for industrial protocol communication.
type Driver interface {
	// Lifecycle
	Init(ctx context.Context, config DriverConfig) error
	Start(ctx context.Context) error
	Stop() error
	Restart(ctx context.Context, config DriverConfig) error

	// Data operations
	Read(ctx context.Context, tags []string) ([]TagValue, error)
	Write(ctx context.Context, commands []WriteCommand) ([]WriteResult, error)
	Subscribe(ctx context.Context, tags []string) (<-chan DataPoint, error)

	// Metadata
	Name() string
	Type() string
	Status() DriverStatus
	Capabilities() DriverCapabilities
}

// DriverConfig holds configuration for a driver instance.
type DriverConfig struct {
	Name     string         `yaml:"name"`
	Type     string         `yaml:"type"`
	Settings map[string]any `yaml:"settings"`
	Tags     []TagConfig    `yaml:"tags"`

	// TagsFile loads tags from an external YAML file instead of (or in
	// addition to) the inline tags list. The file contains a YAML list of
	// TagConfig entries. This is useful when a driver has hundreds of
	// collection points and the main config file becomes unwieldy.
	//
	// If both Tags and TagsFile are set, the inline tags are appended
	// after the file-loaded tags. Tag names must be unique across both
	// sources.
	TagsFile string `yaml:"tags-file,omitempty"`

	// TagsInterval is the hot-reload interval for the tags file.
	// When set (e.g. "30s"), the engine periodically re-reads the file
	// and hot-swaps the driver's tags if the content changed.
	// Empty or "0" means no auto-reload (tags loaded once at startup).
	TagsInterval string `yaml:"tags-interval,omitempty"`
}

// DriverStatus reports the current status of a driver.
type DriverStatus struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	State      ConnState `json:"state"`
	LastRead   time.Time `json:"last_read"`
	LastError  string    `json:"last_error"`
	TagCount   int       `json:"tag_count"`
	ReadCount  uint64    `json:"read_count"`
	ErrorCount uint64    `json:"error_count"`
}

// DriverCapabilities declares what a driver supports.
type DriverCapabilities struct {
	CanRead      bool `json:"can_read"`
	CanWrite     bool `json:"can_write"`
	CanSubscribe bool `json:"can_subscribe"`
	BatchRead    bool `json:"batch_read"`
	MaxBatchSize int  `json:"max_batch_size"`
}
