package testutil

import "time"

// WaitFor polls check every 5ms until it returns true or the timeout
// elapses. Shared by engine and e2e tests instead of duplicated in each.
func WaitFor(timeout time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
