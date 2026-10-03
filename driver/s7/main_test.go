package s7

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all tests
// in the s7 driver package (PERF-007).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
