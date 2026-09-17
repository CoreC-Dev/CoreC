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
	drained, err := ob.Drain(func(pts []core.DataPoint) error {
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
	drained, _ := ob.Drain(func(pts []core.DataPoint) error {
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

	// Push 5 batches, capacity is 3
	for i := 0; i < 5; i++ {
		if err := ob.Push(points, "mqtt"); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}

	if got := ob.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3 (after eviction)", got)
	}

	// Drain should get the 3 newest (seq 3, 4, 5)
	var seqs []uint64
	ob.Drain(func(pts []core.DataPoint) error {
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
	drained, err := ob.Drain(func([]core.DataPoint) error { return nil })
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
	drained, err = ob.Drain(func([]core.DataPoint) error {
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
	drained, err = ob.Drain(func([]core.DataPoint) error { return nil })
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
