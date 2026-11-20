package route

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestAnnotateStalenessDisabled(t *testing.T) {
	tags := map[string]core.DataPoint{
		"temp": {Tag: "temp", Value: 42.5, Timestamp: time.Now()},
	}
	result := annotateStaleness(tags, 0)
	for k, v := range result {
		if v.IsStale {
			t.Errorf("entry %q should not be stale with threshold=0", k)
		}
	}
}

func TestAnnotateStalenessEmpty(t *testing.T) {
	tags := map[string]core.DataPoint{}
	result := annotateStaleness(tags, 30*time.Second)
	if len(result) != 0 {
		t.Fatalf("len(result) = %d, want 0", len(result))
	}
}

func TestAnnotateStalenessMixed(t *testing.T) {
	now := time.Now()
	tags := map[string]core.DataPoint{
		"fresh":   {Tag: "fresh", Value: 1.0, Timestamp: now},
		"stale":   {Tag: "stale", Value: 2.0, Timestamp: now.Add(-2 * time.Minute)},
		"veryold": {Tag: "veryold", Value: 3.0, Timestamp: now.Add(-10 * time.Minute)},
	}

	result := annotateStaleness(tags, 30*time.Second)

	if result["fresh"].IsStale {
		t.Error("fresh should not be stale")
	}
	if !result["stale"].IsStale {
		t.Error("stale should be marked stale")
	}
	if !result["veryold"].IsStale {
		t.Error("veryold should be marked stale")
	}
}

func TestAnnotateStalenessDoesNotMutateOriginal(t *testing.T) {
	now := time.Now()
	tags := map[string]core.DataPoint{
		"stale": {Tag: "stale", Value: 2.0, Timestamp: now.Add(-2 * time.Minute)},
	}

	result := annotateStaleness(tags, 30*time.Second)

	if tags["stale"].IsStale {
		t.Error("original map was mutated: IsStale should still be false")
	}
	if !result["stale"].IsStale {
		t.Error("result map should have IsStale=true")
	}
}

func TestAnnotateStalenessAllFresh(t *testing.T) {
	now := time.Now()
	tags := map[string]core.DataPoint{
		"a": {Tag: "a", Timestamp: now},
		"b": {Tag: "b", Timestamp: now.Add(-10 * time.Second)},
	}

	result := annotateStaleness(tags, 30*time.Second)

	for k, v := range result {
		if v.IsStale {
			t.Errorf("%s should not be stale", k)
		}
	}
}
