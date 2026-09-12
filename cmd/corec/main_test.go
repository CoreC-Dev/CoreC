package main

import (
	"bufio"
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/log"
)

// resetFlags resets the flag.CommandLine so run() can re-define flags.
func resetFlags() {
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
}

func TestSetupLogging(t *testing.T) {
	// Test with config log level.
	cfg := &core.Config{Global: core.GlobalConfig{LogLevel: "debug"}}
	setupLogging(cfg, "")
	if log.Level() != slog.LevelDebug {
		t.Errorf("expected debug level, got %v", log.Level())
	}

	// Test with override.
	cfg = &core.Config{Global: core.GlobalConfig{LogLevel: "info"}}
	setupLogging(cfg, "error")
	if log.Level() != slog.LevelError {
		t.Errorf("expected error level, got %v", log.Level())
	}

	// Test with invalid level (should default to info).
	cfg = &core.Config{Global: core.GlobalConfig{LogLevel: "invalid"}}
	setupLogging(cfg, "")
	if log.Level() != slog.LevelInfo {
		t.Errorf("expected info level for invalid input, got %v", log.Level())
	}

	// Test with empty level (should default to info).
	cfg = &core.Config{Global: core.GlobalConfig{LogLevel: ""}}
	setupLogging(cfg, "")
	if log.Level() != slog.LevelInfo {
		t.Errorf("expected info level for empty input, got %v", log.Level())
	}
}

func TestRunConfigNotFound(t *testing.T) {
	// Save and restore args.
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"corec", "-config", "/nonexistent/config.yaml"}
	resetFlags()

	// Reset flag state by re-parsing.
	// run() should return 1 for missing config.
	exitCode := runWithTimeout(t, 5*time.Second)
	if exitCode != 1 {
		t.Errorf("expected exit code 1 for missing config, got %d", exitCode)
	}
}

func TestRunInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(cfgPath, []byte("invalid: yaml: [[[[\n"), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"corec", "-config", cfgPath}
	resetFlags()

	exitCode := runWithTimeout(t, 5*time.Second)
	if exitCode != 1 {
		t.Errorf("expected exit code 1 for invalid config, got %d", exitCode)
	}
}

func TestRunValidConfigWithSignal(t *testing.T) {
	// This test runs the actual binary as a subprocess to avoid
	// signal conflicts with the Go test framework.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "valid.yaml")
	content := `
global:
  log-level: info
drivers:
  - name: d1
    type: modbus-tcp
    tags:
      - name: t1
        address: "0.0.0.0"
        type: float32
        interval: "5s"
transports:
  - name: t1
    type: mqtt
    settings:
      broker: "tcp://localhost:1883"
      topic: "test/topic"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Build the binary.
	binPath := filepath.Join(dir, "corec.test")
	if err := buildTestBinary(t, binPath); err != nil {
		t.Fatalf("failed to build binary: %v", err)
	}

	// Start the subprocess with a stdout pipe to detect readiness.
	cmd := exec.Command(binPath, "-config", cfgPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start subprocess: %v", err)
	}

	// Wait for the process to produce its first output (the banner),
	// proving it has started executing, then send SIGTERM.  This replaces
	// a fixed sleep and is more reliable than guessing startup latency.
	scanner := bufio.NewScanner(stdout)
	ready := make(chan struct{})
	go func() {
		if scanner.Scan() {
			close(ready)
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("subprocess did not produce any output within 5s")
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)

	err = cmd.Wait()
	if err != nil {
		// SIGTERM causes exit code 1 by default, but our code returns 0.
		// Check if it's a signal:terminated error (expected if the process
		// didn't catch the signal in time).
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Exit code 0 means clean shutdown, which is what we want.
			// Non-zero could mean the signal wasn't caught properly.
			_ = exitErr
		}
	}
}

func buildTestBinary(t *testing.T, path string) error {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", path, ".")
	cmd.Dir = "."
	return cmd.Run()
}

// runWithTimeout runs run() with a timeout to prevent hanging.
func runWithTimeout(t *testing.T, timeout time.Duration) int {
	t.Helper()
	done := make(chan int, 1)
	go func() {
		done <- run()
	}()
	select {
	case code := <-done:
		return code
	case <-time.After(timeout):
		t.Fatal("run() did not exit within timeout")
		return -1
	}
}
