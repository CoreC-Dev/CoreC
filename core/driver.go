package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrSubscribeNotSupported is returned by drivers that don't support subscription mode.
var ErrSubscribeNotSupported = errors.New("subscribe not supported by this driver")

// ErrWritePaced marks a write refused by the driver-level pacing floor
// (settings.write-min-interval): the device accepted a write too recently.
// The command itself is sound, so control planes answer it with a
// client-retryable status (HTTP 429) and dead-letter it with the reason
// intact rather than reporting consequence-less success or an internal
// failure.
var ErrWritePaced = errors.New("write paced")

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

// MergeFileAndInlineTags applies the single merge rule shared by startup
// loading and the tags-file watcher: file tags come first, then the authored
// inline tags. InlineTags is preserved so raw-config projection can write
// back only what the operator authored (the file is re-expanded on load).
func MergeFileAndInlineTags(dc *DriverConfig, fileTags []TagConfig) error {
	dc.InlineTags = dc.Tags
	if dc.InlineTags == nil {
		dc.InlineTags = []TagConfig{}
	}
	seen := make(map[string]struct{}, len(fileTags)+len(dc.InlineTags))
	for _, t := range fileTags {
		if _, dup := seen[t.Name]; dup {
			return fmt.Errorf("driver %s: duplicate tag name %q in tags file", dc.Name, t.Name)
		}
		seen[t.Name] = struct{}{}
	}
	for _, t := range dc.InlineTags {
		if _, dup := seen[t.Name]; dup {
			return fmt.Errorf("driver %s: tag %q is defined by both the tags file and inline tags", dc.Name, t.Name)
		}
		seen[t.Name] = struct{}{}
	}
	merged := make([]TagConfig, 0, len(fileTags)+len(dc.InlineTags))
	merged = append(merged, fileTags...)
	merged = append(merged, dc.InlineTags...)
	dc.Tags = merged
	return nil
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
	// TagsFileResolved is the absolute path actually read: a relative
	// TagsFile is resolved against the config file's directory, not the
	// process working directory. Runtime-only (never written back).
	TagsFileResolved string `yaml:"-" json:"-"`

	// TagsInterval is the hot-reload interval for the tags file.
	// When set (e.g. "30s"), the engine periodically re-reads the file
	// and hot-swaps the driver's tags if the content changed.
	// Empty or "0" means no auto-reload (tags loaded once at startup).
	TagsInterval string `yaml:"tags-interval,omitempty"`

	// InlineTags is the inline tags list exactly as authored, before
	// tags-file expansion. Runtime-only: the raw-config projection and the
	// hot-reload watcher use it to rebuild "file tags + inline tags"
	// without persisting expanded duplicates back to the source YAML.
	InlineTags []TagConfig `yaml:"-" json:"-"`
}

// DriverStatus reports the current status of a driver.
type DriverStatus struct {
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	State          ConnState `json:"state"`
	LastRead       time.Time `json:"last_read"`
	LastError      string    `json:"last_error"`
	TagCount       int       `json:"tag_count"`
	ReadCount      uint64    `json:"read_count"`
	ErrorCount     uint64    `json:"error_count"`
	ReconnectCount uint64    `json:"reconnect_count"`
	// Endpoint is a compact connection target (host:port, serial device,
	// OPC endpoint, ...) so UIs can render topology/connection summaries
	// from status alone instead of parsing raw YAML.
	Endpoint string `json:"endpoint,omitempty"`
}

// DriverCapabilities declares what a driver supports.
type DriverCapabilities struct {
	CanRead      bool `json:"can_read"`
	CanWrite     bool `json:"can_write"`
	CanSubscribe bool `json:"can_subscribe"`
	BatchRead    bool `json:"batch_read"`
	MaxBatchSize int  `json:"max_batch_size"`
}
