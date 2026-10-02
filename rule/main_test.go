package rule

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all tests
// in the rule package. This catches issues like rule provider reload
// loops that are not stopped on engine shutdown.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
