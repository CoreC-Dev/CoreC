package driverbase

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestGetStateLockedUnderHeldRLockAndQueuedWriter pins the re-entrancy
// discipline: Read/Write entries hold the driver's RLock
// and must use the non-locking GetStateLocked — a self-locking GetState()
// here is an RLock-under-RLock that
// deadlocks permanently whenever a writer (an error-path b.Lock()) queues
// between the two acquisitions: the inner RLock waits for the writer, the
// writer waits for the outer RLock, and one parked goroutine silently
// wedges every reader plus /stats, /drivers, and /healthz/ready behind it
// (e.g. on a host that suspends mid-cycle: parked for the whole suspend,
// zero errors logged, healthz/live still saying "alive").
//
// GetStateLocked must not acquire ANY lock, so it stays safe under a held
// RLock with a writer queued.
func TestGetStateLockedUnderHeldRLockAndQueuedWriter(t *testing.T) {
	d := &BaseDriver{name: "test", driverType: "modbus-tcp", config: core.DriverConfig{}}

	// Simulate Read's outer RLock hold.
	d.RLock()
	defer d.RUnlock()

	// Queue a writer (the error-path b.Lock()). It blocks until our
	// RLock releases — exactly the wedged window.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		d.Lock()
		defer d.Unlock()
	}()

	// Give the writer time to actually queue on the mutex.
	time.Sleep(50 * time.Millisecond)

	// The fixed accessor: must return immediately despite the held
	// RLock and the queued writer. The old GetState() here would block
	// forever (inner RLock waits for the writer, writer waits for the
	// outer RLock held by THIS goroutine).
	done := make(chan core.ConnState, 1)
	go func() {
		done <- d.GetStateLocked()
	}()
	select {
	case got := <-done:
		if got != core.StateDisconnected {
			t.Errorf("GetStateLocked returned %v, want StateDisconnected", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GetStateLocked blocked under a held RLock with a queued writer — re-entrant deadlock regression")
	}

	// Release (via defer) lets the writer finish; wait so the test
	// goroutine doesn't leak into others.
	d.RUnlock()
	defer d.RLock()
	select {
	case <-writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("queued writer never acquired after RLock release")
	}
}
