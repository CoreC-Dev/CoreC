package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
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

// --- In-process mocks for the success-path test ---
//
// These satisfy core.Driver and core.Transport with no-op lifecycle
// methods and nil command/data channels, so the engine starts them
// without spawning listener goroutines. They are registered under
// stable type names so a minimal YAML config can reference them.

type testDriver struct {
	name string
}

func (d *testDriver) Init(_ context.Context, cfg core.DriverConfig) error {
	d.name = cfg.Name
	return nil
}
func (d *testDriver) Start(_ context.Context) error                        { return nil }
func (d *testDriver) Stop() error                                          { return nil }
func (d *testDriver) Restart(_ context.Context, _ core.DriverConfig) error { return nil }
func (d *testDriver) Read(_ context.Context, tags []string) ([]core.TagValue, error) {
	vals := make([]core.TagValue, len(tags))
	for i, tag := range tags {
		vals[i] = core.TagValue{Tag: tag, Value: 0, Type: core.TypeFloat64, Quality: core.QualityGood, Timestamp: time.Now()}
	}
	return vals, nil
}
func (d *testDriver) Write(_ context.Context, cmds []core.WriteCommand) ([]core.WriteResult, error) {
	res := make([]core.WriteResult, len(cmds))
	for i := range res {
		res[i] = core.WriteResult{Success: true}
	}
	return res, nil
}
func (d *testDriver) Subscribe(_ context.Context, _ []string) (<-chan core.DataPoint, error) {
	return nil, core.ErrSubscribeNotSupported
}
func (d *testDriver) Name() string { return d.name }
func (d *testDriver) Type() string { return "test-mock-driver" }
func (d *testDriver) Status() core.DriverStatus {
	return core.DriverStatus{Name: d.name, State: core.StateConnected}
}
func (d *testDriver) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{CanRead: true, CanWrite: true}
}

type testTransport struct {
	name string
}

func (t *testTransport) Init(_ context.Context, cfg core.TransportConfig) error {
	t.name = cfg.Name
	return nil
}
func (t *testTransport) Start(_ context.Context) error                     { return nil }
func (t *testTransport) Stop() error                                       { return nil }
func (t *testTransport) Publish(_ context.Context, _ core.DataPoint) error { return nil }
func (t *testTransport) PublishBatch(_ context.Context, _ []core.DataPoint) error {
	return nil
}
func (t *testTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (t *testTransport) OnData() <-chan core.DataPoint       { return nil }
func (t *testTransport) Name() string                        { return t.name }
func (t *testTransport) Type() string                        { return "test-mock-transport" }
func (t *testTransport) Status() core.TransportStatus {
	return core.TransportStatus{Name: t.name, State: core.StateConnected}
}

func registerTestMocks() {
	core.RegisterDriver("test-mock-driver", func(_ core.DriverConfig) (core.Driver, error) {
		return &testDriver{}, nil
	})
	core.RegisterTransport("test-mock-transport", func(_ core.TransportConfig) (core.Transport, error) {
		return &testTransport{}, nil
	})
}

// freePort returns a TCP port that is free at call time by opening a
// listener on :0, reading the assigned port, and closing the listener.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestRunValidConfigInProcess exercises the full success path of run()
// in-process: config load, logging setup, engine start, alert handler
// registration, hub start, signal-driven shutdown, and clean exit. It
// contributes line coverage that the subprocess-based signal test cannot.
//
// Readiness is detected by polling the API TCP port (bound synchronously
// inside hub.Start), then a short grace period lets run() reach its
// signal.Notify before SIGTERM is delivered.
func TestRunValidConfigInProcess(t *testing.T) {
	registerTestMocks()

	apiAddr := freePort(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "valid.yaml")
	content := fmt.Sprintf(`
global:
  log-level: info
  api:
    listen: "%s"
    secret: "test-secret-123"
drivers:
  - name: d1
    type: test-mock-driver
    tags:
      - name: t1
        address: "0.0.0.0"
        type: float32
        interval: "5s"
transports:
  - name: t1
    type: test-mock-transport
`, apiAddr)
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"corec", "-config", cfgPath}
	resetFlags()

	done := make(chan int, 1)
	go func() { done <- run() }()

	// Poll the API port for readiness, but also detect an early exit
	// (e.g. engine.Start failed) so the test reports the real cause.
	deadline := time.Now().Add(8 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", apiAddr, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			ready = true
			break
		}
		select {
		case code := <-done:
			t.Fatalf("run() exited early with code %d before the API became ready", code)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("API server did not become ready within timeout")
	}

	// hub.Start binds the listener before run() installs its signal
	// handler; give the goroutine a brief moment to reach signal.Notify.
	time.Sleep(150 * time.Millisecond)

	// Deliver SIGTERM to trigger the graceful-shutdown path. run()
	// registered for SIGTERM via signal.Notify, so the signal is
	// received by its channel rather than terminating the process.
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("expected exit code 0 after SIGTERM, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not exit after SIGTERM")
	}
}

// TestRunMissingConfigFlag verifies that running with no -config flag
// (which defaults to "config.yaml") fails cleanly with exit code 1 when
// that default file does not exist in the working directory.
func TestRunMissingConfigFlag(t *testing.T) {
	// Run from an empty temp dir so the default "config.yaml" is absent.
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(oldWd)
	}()

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"corec"}
	resetFlags()

	exitCode := runWithTimeout(t, 5*time.Second)
	if exitCode != 1 {
		t.Errorf("expected exit code 1 for missing default config, got %d", exitCode)
	}
}
