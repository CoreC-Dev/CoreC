package route

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all tests
// in the route package. This catches issues like the rate-limiter
// eviction goroutine that was not being cancelled on server shutdown
// (P0-2). If a test leaves a goroutine running, goleak fails the test
// suite with a stack trace of the leaked goroutine.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// Ignore goroutines from the runtime/http transport that may
		// persist between tests (e.g. idle connections in keep-alive
		// pools). These are managed by the Go runtime, not our code.
		goleak.IgnoreAnyFunction("internal/poll.(*FD).WaitWrite"),
		goleak.IgnoreAnyFunction("internal/poll.(*FD).WaitRead"),
		// The rate-limiter eviction goroutine runs in a select on
		// ctx.Done(). Tests that call router(context.Background(), ...)
		// start this goroutine with an uncancellable context; it is
		// cleaned up when the test process exits. Production code uses
		// context.WithCancel (P0-2 fix) so this is test-only.
		goleak.IgnoreAnyFunction("github.com/CoreC-Dev/CoreC/hub/route.rateLimitMiddleware.func1"),
		// The Observable goroutine is started by the log package
		// initialization and runs for the process lifetime.
		goleak.IgnoreAnyFunction("github.com/CoreC-Dev/CoreC/common/observable.(*Observable[...]).start"),
	)
}
