// Package testutil provides shared test helpers for driver reconnect behavior.
// Import only from _test.go files.
package testutil

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// PollReconnectCount polls Status().ReconnectCount until it exceeds zero,
// proving the counted reconnect loop is wired up and incrementing.
// Returns the first non-zero count, or fails the test if the deadline passes.
func PollReconnectCount(t *testing.T, d core.Driver, timeout time.Duration) uint64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c := d.Status().ReconnectCount
		if c > 0 {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ReconnectCount never incremented; reconnect loop did not make any counted attempts")
	return 0
}

// PollReconnectCountGrowing polls until ReconnectCount exceeds first,
// confirming the loop is still retrying. Fails if the counter does not
// advance within the timeout.
func PollReconnectCountGrowing(t *testing.T, d core.Driver, first uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.Status().ReconnectCount > first {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := d.Status().ReconnectCount; got <= first {
		t.Errorf("expected ReconnectCount to keep growing, first=%d now=%d", first, got)
	}
}

// PollUntilConnected polls Status().State until it equals StateConnected.
// Returns true if connected within the timeout, false otherwise.
func PollUntilConnected(t *testing.T, d core.Driver, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.Status().State == core.StateConnected {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
