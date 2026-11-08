//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"
)

// sendSIGTERM delivers SIGTERM to this process so run()'s signal handler fires.
func sendSIGTERM(t *testing.T) bool {
	t.Helper()
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	return true
}

// terminateProcess asks a subprocess to shut down gracefully.
func terminateProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
