//go:build windows

package main

import (
	"os"
	"testing"
)

// sendSIGTERM is unsupported on Windows: os.Process.Signal cannot deliver
// SIGTERM, so the graceful-shutdown test is skipped instead of asserting a
// path Windows cannot exercise. The POSIX path is covered on Linux CI.
func sendSIGTERM(t *testing.T) bool {
	t.Helper()
	t.Skip("SIGTERM delivery is POSIX-only; graceful shutdown is covered on Linux CI")
	return false
}

// terminateProcess has no graceful SIGTERM on Windows; terminate the child.
func terminateProcess(p *os.Process) error {
	return p.Kill()
}
