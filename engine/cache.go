package engine

import (
	"hash/fnv"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/CoreC-Dev/CoreC/core"
)

const numCacheShards = 64

// LatestCache maintains the most recent value for each tag.
// It is sharded by driver name to reduce mutex contention when
// multiple processing workers update the cache concurrently.
type LatestCache struct {
	shards [numCacheShards]cacheShard
}

type cacheShard struct {
	mu      sync.RWMutex
	drivers map[string]*driverCache
}

// driverCache holds values for one driver and maintains an
// atomic snapshot so that GetByDriver avoids a full map copy
// when the data hasn't changed since the last snapshot.
type driverCache struct {
	mu       sync.RWMutex
	values   map[string]core.DataPoint
	snapshot atomic.Pointer[map[string]core.DataPoint]
	dirty    atomic.Bool
}

func NewLatestCache() *LatestCache {
	c := &LatestCache{}
	for i := range c.shards {
		c.shards[i].drivers = make(map[string]*driverCache)
	}
	return c
}

func cacheShardIdx(driver string) int {
	h := fnv.New32a()
	h.Write([]byte(driver))
	return int(h.Sum32() % numCacheShards)
}

func (c *LatestCache) Update(point core.DataPoint) {
	idx := cacheShardIdx(point.Driver)
	shard := &c.shards[idx]

	shard.mu.RLock()
	dc, ok := shard.drivers[point.Driver]
	shard.mu.RUnlock()

	if !ok {
		shard.mu.Lock()
		dc, ok = shard.drivers[point.Driver]
		if !ok {
			dc = &driverCache{values: make(map[string]core.DataPoint)}
			shard.drivers[point.Driver] = dc
		}
		shard.mu.Unlock()
	}

	dc.mu.Lock()
	dc.values[point.Tag] = point
	// Set dirty under the same lock that publishes the value: a reader
	// between Unlock and Store would see the new value but a false dirty
	// flag and take the stale fast-path snapshot.
	dc.dirty.Store(true)
	dc.mu.Unlock()
}

// Get retrieves the latest value for a specific driver and tag.
func (c *LatestCache) Get(driver, tag string) (core.DataPoint, bool) {
	idx := cacheShardIdx(driver)
	shard := &c.shards[idx]

	shard.mu.RLock()
	dc, ok := shard.drivers[driver]
	shard.mu.RUnlock()
	if !ok {
		return core.DataPoint{}, false
	}

	dc.mu.RLock()
	defer dc.mu.RUnlock()
	point, ok := dc.values[tag]
	return point, ok
}

// GetByDriver retrieves all latest values for a driver.
// It returns an atomic snapshot that is reused across calls
// when the data hasn't changed, avoiding a full map copy.
func (c *LatestCache) GetByDriver(driver string) map[string]core.DataPoint {
	idx := cacheShardIdx(driver)
	shard := &c.shards[idx]

	shard.mu.RLock()
	dc, ok := shard.drivers[driver]
	shard.mu.RUnlock()
	if !ok {
		return nil
	}

	if !dc.dirty.Load() {
		if p := dc.snapshot.Load(); p != nil {
			return *p
		}
	}

	// Slow path: rebuild snapshot under the write lock so that no
	// concurrent Update can race between our map copy and the
	// snapshot store / dirty clear. Without the write lock here,
	// an Update that sets dirty=true after our copy but before we
	// store dirty=false would be lost, leaving a stale snapshot.
	dc.mu.Lock()
	snap := make(map[string]core.DataPoint, len(dc.values))
	for k := range dc.values {
		snap[k] = dc.values[k]
	}
	dc.snapshot.Store(&snap)
	dc.dirty.Store(false)
	dc.mu.Unlock()
	return snap
}

// GetAll retrieves the latest values for every driver, keyed by tag name.
// When two drivers share a tag name, the last driver in sorted-name order
// wins (the DataPoint value carries its own Driver field, so the source is
// not lost). Sorting makes the winner deterministic across calls.
func (c *LatestCache) GetAll() map[string]core.DataPoint {
	all := make(map[string]core.DataPoint)
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.RLock()
		names := make([]string, 0, len(shard.drivers))
		for name := range shard.drivers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			dc := shard.drivers[name]
			dc.mu.RLock()
			for k := range dc.values {
				all[k] = dc.values[k]
			}
			dc.mu.RUnlock()
		}
		shard.mu.RUnlock()
	}
	return all
}

// DeleteDriver evicts every cached value of the named driver. Called on
// RemoveDriver so a deleted driver's last values stop appearing in
// GET /tags and derived-tag inputs.
func (c *LatestCache) DeleteDriver(driver string) {
	idx := cacheShardIdx(driver)
	shard := &c.shards[idx]

	shard.mu.Lock()
	if dc, ok := shard.drivers[driver]; ok {
		delete(shard.drivers, driver)
		// Drop the atomic snapshot too: GetByDriver would otherwise
		// keep serving the deleted driver's last snapshot until the
		// next rebuild (which would never come — no more Updates).
		dc.mu.Lock()
		dc.snapshot.Store(nil)
		dc.dirty.Store(false)
		dc.mu.Unlock()
	}
	shard.mu.Unlock()
}

// DeleteTag evicts one cached value. Called by the tags-file reload
// path for tags that disappeared from the file.
func (c *LatestCache) DeleteTag(driver, tag string) {
	idx := cacheShardIdx(driver)
	shard := &c.shards[idx]

	shard.mu.RLock()
	dc, ok := shard.drivers[driver]
	shard.mu.RUnlock()
	if !ok {
		return
	}

	dc.mu.Lock()
	delete(dc.values, tag)
	dc.mu.Unlock()
	// Force the snapshot rebuild on the next GetByDriver so the
	// eviction is visible to the snapshot fast path too.
	dc.dirty.Store(true)
}

func (c *LatestCache) Clear() {
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		shard.drivers = make(map[string]*driverCache)
		shard.mu.Unlock()
	}
}
