package engine

import (
	"hash/fnv"
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

// NewLatestCache creates a new LatestCache.
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

// Update stores or updates a DataPoint in the cache.
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
	dc.mu.Unlock()
	dc.dirty.Store(true)
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

	// Fast path: return cached snapshot if not dirty
	if !dc.dirty.Load() {
		if p := dc.snapshot.Load(); p != nil {
			return *p
		}
	}

	// Slow path: rebuild snapshot
	dc.mu.RLock()
	snap := make(map[string]core.DataPoint, len(dc.values))
	for k := range dc.values {
		snap[k] = dc.values[k]
	}
	dc.mu.RUnlock()

	dc.snapshot.Store(&snap)
	dc.dirty.Store(false)
	return snap
}

// GetAll retrieves the latest values for every driver, keyed by tag name.
// When two drivers share a tag name, the last one visited wins (the
// DataPoint value carries its own Driver field, so the source is not lost).
// This is the backing implementation for the GET /tags endpoint.
func (c *LatestCache) GetAll() map[string]core.DataPoint {
	all := make(map[string]core.DataPoint)
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.RLock()
		for _, dc := range shard.drivers {
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

// Clear removes all cached data.
func (c *LatestCache) Clear() {
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		shard.drivers = make(map[string]*driverCache)
		shard.mu.Unlock()
	}
}
