package httppush

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all tests
// in the httppush transport package (PERF-007).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
