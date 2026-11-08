package engine

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// GetAll returns the latest value for every tag across all
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
	// Sorted driver names make the winner deterministic: drv2 is visited
	// last and overwrites drv1.
	dp := all["temp"]
	if dp.Value != 20.0 || dp.Driver != "drv2" {
		t.Errorf("temp = %v from %q, want 20.0 from drv2 (deterministic last in sorted order)", dp.Value, dp.Driver)
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
