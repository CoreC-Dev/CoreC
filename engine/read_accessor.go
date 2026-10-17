// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// readAccessor groups the synchronous data-access API: on-demand tag
// reads (ReadTag), cached latest values (LatestValues), and the cached
// staleness threshold (StaleThreshold). Extracted from CoreCEngine in
// the CPLX-020 God Object split; CoreCEngine keeps thin forwarders so
// the core.DataAccessor and core.StatsProvider method sets are
// unchanged.
type readAccessor struct {
	// staleThreshold is stored as nanoseconds in an atomic so that
	// StaleThreshold() can be read lock-free while applyEngineConfig
	// writes it during Reload/Start without a data race.
	staleThreshold atomic.Int64 // nanoseconds; 0 = disabled

	// eng is the owning engine: ReadTag reaches the driver registry and
	// the shared lock through it; LatestValues reaches the latest cache.
	eng *CoreCEngine
}

// ReadTag reads a single tag value synchronously from its driver.
func (a *readAccessor) ReadTag(ctx context.Context, driver, tag string) (*core.TagValue, error) {
	a.eng.mu.RLock()
	d, ok := a.eng.drivers[driver]
	a.eng.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver not found: %s", driver)
	}

	values, err := d.Read(ctx, []string{tag})
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no value returned for tag: %s", tag)
	}
	return &values[0], nil
}

// LatestValues returns the cached latest values for one driver, or for
// every driver when driver is empty (this backs the GET /tags endpoint).
func (a *readAccessor) LatestValues(driver string) map[string]core.DataPoint {
	if driver == "" {
		// Aggregate every driver; this backs the GET /tags endpoint.
		return a.eng.cache.GetAll()
	}
	return a.eng.cache.GetByDriver(driver)
}

// StaleThreshold returns the configured staleness threshold for cached
// values (0 if disabled).
func (a *readAccessor) StaleThreshold() time.Duration {
	// Read atomically: this is called from HTTP handlers concurrently
	// with applyEngineConfig writes during Reload/Start.
	return time.Duration(a.staleThreshold.Load())
}
