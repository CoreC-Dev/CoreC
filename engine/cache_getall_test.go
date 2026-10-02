package engine

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// TEST-011: GetAll returns the latest value for every tag across all
// drivers and shards. When two drivers share a tag name, the last one
// visited wins (the DataPoint carries its own Driver field).

func TestCacheGetAllEmpty(t *testing.T) {
	c := NewLatestCache()
	all := c.GetAll()
	if len(all) != 0 {
		t.Errorf("expected empty map, got %d entries", len(all))
	}
}

func TestCacheGetAllSingleDriver(t *testing.T) {
	c := NewLatestCache()
	c.Update(core.DataPoint{Driver: "drv1", Tag: "temp", Value: 42.5})
	c.Update(core.DataPoint{Driver: "drv1", Tag: "humid", Value: 60.0})

	all := c.GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	if all["temp"].Value != 42.5 {
		t.Errorf("temp = %v, want 42.5", all["temp"].Value)
	}
	if all["humid"].Value != 60.0 {
		t.Errorf("humid = %v, want 60.0", all["humid"].Value)
	}
}

func TestCacheGetAllMultipleDriversSharedTag(t *testing.T) {
	c := NewLatestCache()
	c.Update(core.DataPoint{Driver: "drv1", Tag: "temp", Value: 10.0})
	c.Update(core.DataPoint{Driver: "drv2", Tag: "temp", Value: 20.0})

	all := c.GetAll()
	if len(all) != 1 {
		t.Fatalf("expected 1 entry (shared tag), got %d", len(all))
	}
	// The value should be one of the two drivers' values; the DataPoint
	// carries its own Driver field so the source is identifiable.
	dp := all["temp"]
	if dp.Value != 10.0 && dp.Value != 20.0 {
		t.Errorf("temp value = %v, want 10.0 or 20.0", dp.Value)
	}
}

func TestCacheGetAllReflectsUpdates(t *testing.T) {
	c := NewLatestCache()
	c.Update(core.DataPoint{Driver: "drv1", Tag: "count", Value: 1})
	c.Update(core.DataPoint{Driver: "drv1", Tag: "count", Value: 2})

	all := c.GetAll()
	if all["count"].Value != 2 {
		t.Errorf("expected latest value 2, got %v", all["count"].Value)
	}
}

func TestCacheGetAllAfterClear(t *testing.T) {
	c := NewLatestCache()
	c.Update(core.DataPoint{Driver: "drv1", Tag: "x", Value: 1})
	c.Clear()

	all := c.GetAll()
	if len(all) != 0 {
		t.Errorf("expected empty after Clear, got %d", len(all))
	}
}
