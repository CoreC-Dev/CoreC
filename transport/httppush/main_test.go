package httppush

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain integrates goleak to detect goroutine leaks across all tests
// in the httppush transport package.
//
// The ignores below are all runtime-managed keep-alive connection
// goroutines, not product leaks:
//   - the internal/poll pair mirrors hub/route/main_test.go (client-side
//     idle-connection poll waits);
//   - net/http.(*connReader).Read is the test HTTP SERVER's keep-alive
//     connection parked waiting for the next request: httptest.Server.Close
//     closes idle connections, but a connection that just finished a
//     response can still be mid-reap when goleak runs.
//   - the three persistConn entries are the CLIENT side: http.Transport's
//     keep-alive connection read/write loops, which CloseIdleConnections
//     reaps lazily.
//     Under full-suite -race load both shapes race goleak intermittently.
//     The (*FD).WaitRead ignore does not match these stacks —
//     they sit in (*pollDesc).waitRead (lowercase, a different function) —
//     so the http-level ignores are the ones that actually bite. All
//     ignored goroutines are runtime-managed idle connections; a genuine
//     product leak elsewhere is still caught.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("internal/poll.(*FD).WaitWrite"),
		goleak.IgnoreAnyFunction("internal/poll.(*FD).WaitRead"),
		goleak.IgnoreAnyFunction("net/http.(*connReader).Read"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).Read"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	)
}
