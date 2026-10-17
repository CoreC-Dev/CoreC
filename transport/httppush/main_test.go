package httppush

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all tests
// in the httppush transport package (PERF-007).
//
// The two internal/poll ignores mirror hub/route/main_test.go: this
// package exercises real HTTP clients (webhook pushes), whose
// keep-alive idle connections leave runtime-managed poll goroutines
// that the Go runtime closes lazily. They are not product leaks; under
// full-suite -race load their lazy close sometimes outlived the test
// process and made goleak fail intermittently (stage-A audit: 2/2
// failures under load, 3/3 pass in isolation).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("internal/poll.(*FD).WaitWrite"),
		goleak.IgnoreAnyFunction("internal/poll.(*FD).WaitRead"),
	)
}
