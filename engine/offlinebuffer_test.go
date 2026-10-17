package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestOfflineBufferPushDrain(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	points := []core.DataPoint{
		{Driver: "plc1", Tag: "temp", Value: 42.5, Timestamp: time.Now()},
		{Driver: "plc1", Tag: "press", Value: 101.3, Timestamp: time.Now()},
	}

	// Push two batches
	if err := ob.Push(points, "mqtt"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := ob.Push(points, "mqtt"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	if got := ob.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}

	// Drain both
	var drainedPoints [][]core.DataPoint
	drained, err := ob.Drain("mqtt", func(pts []core.DataPoint) error {
		drainedPoints = append(drainedPoints, pts)
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if drained != 2 {
		t.Fatalf("drained = %d, want 2", drained)
	}
	if len(drainedPoints) != 2 {
		t.Fatalf("drainedPoints len = %d, want 2", len(drainedPoints))
	}
	if got := ob.Len(); got != 0 {
		t.Fatalf("Len after drain = %d, want 0", got)
	}
}

func TestOfflineBufferDrainStopsOnError(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	points := []core.DataPoint{{Driver: "plc1", Tag: "temp", Value: 1.0}}
	for i := 0; i < 3; i++ {
		if err := ob.Push(points, "mqtt"); err != nil {
			t.Fatalf("Push: %v", err)
		}
	}

	// Drain should stop after first failure
	callCount := 0
	drained, _ := ob.Drain("mqtt", func(pts []core.DataPoint) error {
		callCount++
		if callCount == 2 {
			return errMockPublish
		}
		return nil
	})
	if drained != 1 {
		t.Fatalf("drained = %d, want 1", drained)
	}
	if got := ob.Len(); got != 2 {
		t.Fatalf("Len after partial drain = %d, want 2", got)
	}
}

var errMockPublish = newMockError("mock publish failure")

type mockError struct{ msg string }

func (e *mockError) Error() string { return e.msg }

func newMockError(msg string) error { return &mockError{msg: msg} }

func TestOfflineBufferEviction(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 3) // small capacity
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	points := []core.DataPoint{{Driver: "plc1", Tag: "temp", Value: 1.0}}

	if got := ob.EvictedCount(); got != 0 {
		t.Fatalf("EvictedCount = %d, want 0 on fresh buffer", got)
	}

	// Push 5 batches, capacity is 3
	for i := 0; i < 5; i++ {
		if err := ob.Push(points, "mqtt"); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}

	if got := ob.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3 (after eviction)", got)
	}

	// B2.1: every eviction must be observable, not silent. 5 pushes into
	// a capacity-3 buffer evict exactly 2 oldest batches.
	if got := ob.EvictedCount(); got != 2 {
		t.Fatalf("EvictedCount = %d, want 2 (5 pushes into capacity 3)", got)
	}

	// Drain should get the 3 newest (seq 3, 4, 5)
	var seqs []uint64
	ob.Drain("mqtt", func(pts []core.DataPoint) error {
		return nil
	})
	// After drain, all should be removed
	if got := ob.Len(); got != 0 {
		t.Fatalf("Len after drain = %d, want 0", got)
	}
	_ = seqs
}

func TestOfflineBufferEmptyPush(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	// Pushing empty slice should be no-op
	if err := ob.Push(nil, "mqtt"); err != nil {
		t.Fatalf("Push(nil): %v", err)
	}
	if got := ob.Len(); got != 0 {
		t.Fatalf("Len = %d, want 0", got)
	}
}

func TestOfflineBufferRestartPreservesSeq(t *testing.T) {
	dir := t.TempDir()
	ob1, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}

	points := []core.DataPoint{{Driver: "plc1", Tag: "temp", Value: 1.0}}
	for i := 0; i < 3; i++ {
		if err := ob1.Push(points, "mqtt"); err != nil {
			t.Fatalf("Push: %v", err)
		}
	}
	ob1.Close()

	// Reopen — should detect existing seq numbers
	ob2, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer (reopen): %v", err)
	}
	defer ob2.Close()

	if got := ob2.Len(); got != 3 {
		t.Fatalf("Len after reopen = %d, want 3", got)
	}

	// Push should continue from where we left off (no collision)
	if err := ob2.Push(points, "mqtt"); err != nil {
		t.Fatalf("Push after reopen: %v", err)
	}
	if got := ob2.Len(); got != 4 {
		t.Fatalf("Len after push = %d, want 4", got)
	}

	// Verify file naming is monotonic
	entries, _ := os.ReadDir(dir)
	var maxSeq uint64
	for _, ent := range entries {
		if seq, ok := parseSeqFile(ent.Name()); ok && seq > maxSeq {
			maxSeq = seq
		}
	}
	if maxSeq < 4 {
		t.Fatalf("maxSeq = %d, want >= 4", maxSeq)
	}
}

func TestOfflineBufferRequiresPath(t *testing.T) {
	_, err := NewOfflineBuffer("", 100)
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestOfflineBufferAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	points := []core.DataPoint{{Driver: "plc1", Tag: "temp", Value: 42.5}}
	if err := ob.Push(points, "mqtt"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// No .tmp files should remain
	entries, _ := os.ReadDir(dir)
	for _, ent := range entries {
		if filepath.Ext(ent.Name()) == ".tmp" {
			t.Fatalf("temp file left behind: %s", ent.Name())
		}
	}
}

// TestOfflineBufferPendingCount verifies that PendingCount mirrors Len
// (the public alias contract) and is thread-safe to call concurrently.
func TestOfflineBufferPendingCount(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	if got := ob.PendingCount(); got != 0 {
		t.Fatalf("PendingCount = %d, want 0 on empty buffer", got)
	}
	if got := ob.PendingCount(); got != ob.Len() {
		t.Fatalf("PendingCount (%d) != Len (%d); alias contract broken", got, ob.Len())
	}

	points := []core.DataPoint{{Driver: "plc1", Tag: "temp", Value: 1.0, Timestamp: time.Now()}}
	for i := 0; i < 4; i++ {
		if err := ob.Push(points, "mqtt"); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}
	if got := ob.PendingCount(); got != 4 {
		t.Fatalf("PendingCount = %d, want 4", got)
	}
	if got := ob.PendingCount(); got != ob.Len() {
		t.Fatalf("PendingCount (%d) != Len (%d) after pushes", got, ob.Len())
	}

	// Concurrent reads must not race or panic. -race in CI exercises this.
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				_ = ob.PendingCount()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// TestOfflineBufferDrainCount verifies that DrainCount tracks the total
// number of batches successfully replayed, including across multiple Drain
// calls and the transport-down pause path (partial drain).
func TestOfflineBufferDrainCount(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	if got := ob.DrainCount(); got != 0 {
		t.Fatalf("DrainCount = %d, want 0 on fresh buffer", got)
	}

	points := []core.DataPoint{{Driver: "plc1", Tag: "temp", Value: 1.0, Timestamp: time.Now()}}

	// Push 3 batches and drain them all successfully.
	for i := 0; i < 3; i++ {
		if err := ob.Push(points, "mqtt"); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}
	drained, err := ob.Drain("mqtt", func([]core.DataPoint) error { return nil })
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if drained != 3 {
		t.Fatalf("drained = %d, want 3", drained)
	}
	if got := ob.DrainCount(); got != 3 {
		t.Fatalf("DrainCount = %d, want 3 after full drain", got)
	}
	if got := ob.Len(); got != 0 {
		t.Fatalf("Len = %d, want 0 after drain", got)
	}

	// Push 3 more; this time the publish callback fails on the 2nd batch,
	// so Drain stops after 1 success (transport-down pause path). DrainCount
	// must still reflect the 1 batch that was replayed.
	for i := 0; i < 3; i++ {
		if err := ob.Push(points, "mqtt"); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}
	callCount := 0
	drained, err = ob.Drain("mqtt", func([]core.DataPoint) error {
		callCount++
		if callCount == 2 {
			return errMockPublish
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1 (paused on 2nd)", drained)
	}
	if got := ob.DrainCount(); got != 4 { // 3 + 1
		t.Fatalf("DrainCount = %d, want 4 after partial drain", got)
	}
	if got := ob.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2 after partial drain", got)
	}

	// Drain the remaining 2 — DrainCount reaches 6.
	drained, err = ob.Drain("mqtt", func([]core.DataPoint) error { return nil })
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if drained != 2 {
		t.Fatalf("drained = %d, want 2", drained)
	}
	if got := ob.DrainCount(); got != 6 {
		t.Fatalf("DrainCount = %d, want 6 after final drain", got)
	}
}

// TestOfflineBufferDrainFiltersBySource verifies the B2.3 ownership
// filter: the buffer is shared across all transport batchers, so a
// drain for transport B must leave transport A's batches on disk —
// replaying them through B would misroute the data and delete the
// file before A ever replays it.
func TestOfflineBufferDrainFiltersBySource(t *testing.T) {
	dir := t.TempDir()
	ob, err := NewOfflineBuffer(dir, 100)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	points := []core.DataPoint{
		{Driver: "plc", Tag: "temp", Value: 1.0, Timestamp: time.Now()},
	}

	// Two transports' batches in one shared buffer.
	if err := ob.Push(points, "mqtt-a"); err != nil {
		t.Fatalf("Push mqtt-a: %v", err)
	}
	if err := ob.Push(points, "http-b"); err != nil {
		t.Fatalf("Push http-b: %v", err)
	}

	// Drain as http-b: must replay only http-b's batch and leave
	// mqtt-a's on disk untouched.
	var publishedTags []string
	drainedB, err := ob.Drain("http-b", func(pts []core.DataPoint) error {
		for _, p := range pts {
			publishedTags = append(publishedTags, p.Tag)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Drain http-b: %v", err)
	}
	if drainedB != 1 {
		t.Fatalf("drainedB = %d, want 1", drainedB)
	}
	if got := ob.Len(); got != 1 {
		t.Fatalf("Len after http-b drain = %d, want 1 (mqtt-a's batch must remain)", got)
	}

	// mqtt-a's batch still replays through ITS OWN drain afterwards.
	drainedA, err := ob.Drain("mqtt-a", func(pts []core.DataPoint) error { return nil })
	if err != nil {
		t.Fatalf("Drain mqtt-a: %v", err)
	}
	if drainedA != 1 {
		t.Fatalf("drainedA = %d, want 1 (own batch must still replay)", drainedA)
	}
	if got := ob.Len(); got != 0 {
		t.Fatalf("Len after mqtt-a drain = %d, want 0", got)
	}

	// A drain for a transport with nothing buffered is a no-op that
	// leaves the disk empty.
	if n, err := ob.Drain("nobody", func([]core.DataPoint) error { return nil }); err != nil || n != 0 {
		t.Fatalf("Drain nobody = (%d, %v), want (0, nil)", n, err)
	}
}
