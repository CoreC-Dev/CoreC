package opcua

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all tests
// in the opcua driver package (PERF-007).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
