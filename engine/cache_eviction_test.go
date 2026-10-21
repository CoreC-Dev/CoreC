package engine

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestCacheEvictionOnRemoveDriver verifies the PIPE-1 fix: removing a
// driver evicts its cached values. Previously the cache was never
// pruned (Clear() is test-only) — a deleted driver's last values kept
// appearing in GET /tags and kept feeding derived-tag expressions
// forever, only ever acquiring an is_stale flag.
func TestCacheEvictionOnRemoveDriver(t *testing.T) {
	e := New().(*CoreCEngine)

	// Seed the cache for two drivers directly: the cache is fed by the
	// processing pipeline, but its contract is independent of drivers.
	seed := func(driver, tag string) {
		e.processing.cache.Update(core.DataPoint{
			Driver: driver, Tag: tag, Value: 1.0,
			Type: core.TypeFloat64, Quality: core.QualityGood, Timestamp: time.Now(),
		})
	}
	seed("plc-gone", "temp")
	seed("plc-kept", "temp")
	// Also seed a snapshot (GetByDriver) for the doomed driver so the
	// fast path is exercised too.
	if got := len(e.processing.cache.GetByDriver("plc-gone")); got != 1 {
		t.Fatalf("GetByDriver before eviction = %d, want 1", got)
	}

	e.processing.cache.DeleteDriver("plc-gone")

	if _, ok := e.processing.cache.Get("plc-gone", "temp"); ok {
		t.Error("deleted driver's value still cached after eviction")
	}
	if got := e.processing.cache.GetByDriver("plc-gone"); len(got) != 0 {
		t.Errorf("deleted driver's snapshot still serves %d values", len(got))
	}
	// The surviving driver is untouched.
	if _, ok := e.processing.cache.Get("plc-kept", "temp"); !ok {
		t.Error("eviction leaked into an unrelated driver's values")
	}
}

// TestCacheEvictionOnTagRemoval verifies the per-tag eviction used by
// the tags-file reload path (PIPE-1): removing a tag evicts exactly
// that entry, leaving sibling tags alone.
func TestCacheEvictionOnTagRemoval(t *testing.T) {
	e := New().(*CoreCEngine)
	cache := e.processing.cache

	for _, tag := range []string{"keep-a", "drop-b", "keep-c"} {
		cache.Update(core.DataPoint{
			Driver: "plc1", Tag: tag, Value: 2.0,
			Type: core.TypeFloat64, Quality: core.QualityGood, Timestamp: time.Now(),
		})
	}
	// Build the snapshot so the eviction must invalidate it too.
	if got := len(cache.GetByDriver("plc1")); got != 3 {
		t.Fatalf("snapshot before eviction = %d, want 3", got)
	}

	cache.DeleteTag("plc1", "drop-b")

	if _, ok := cache.Get("plc1", "drop-b"); ok {
		t.Error("removed tag still cached")
	}
	if got := len(cache.GetByDriver("plc1")); got != 2 {
		t.Errorf("snapshot after eviction = %d, want 2 (must rebuild, not serve the stale snapshot)", got)
	}
	for _, tag := range []string{"keep-a", "keep-c"} {
		if _, ok := cache.Get("plc1", tag); !ok {
			t.Errorf("eviction removed sibling tag %s", tag)
		}
	}

	// Evicting an unknown driver/tag is a safe no-op.
	cache.DeleteTag("no-such-driver", "no-such-tag")
	cache.DeleteDriver("no-such-driver")
}
