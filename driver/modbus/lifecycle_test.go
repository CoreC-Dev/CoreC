package modbus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

// --- Name / Status / Subscribe tests ---

func TestModbusNameStatusSubscribe(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
	}

	handler := &testModbusHandler{
		coils:          map[uint16]bool{0: true},
		holding:        map[uint16]uint16{0: 1234},
		discreteInputs: map[uint16]bool{},
		inputRegs:      map[uint16]uint16{},
	}
	server, err := mb.NewServer(&mb.ServerConfiguration{
		URL:     fmt.Sprintf("tcp://127.0.0.1:%d", port),
		Timeout: 5 * time.Second,
	}, handler)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer server.Stop()
	// server.Start() calls net.Listen synchronously — ready immediately.

	cfg := core.DriverConfig{
		Name: "test-plc",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":     "127.0.0.1",
			"port":     port,
			"slave-id": 1,
			"timeout":  "2s",
		},
		Tags: []core.TagConfig{
			{Name: "temp", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusTCPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}
	ctx := context.Background()
	if err := drv.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := drv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer drv.Stop()

	// Name
	if drv.Name() != "test-plc" {
		t.Errorf("expected name 'test-plc', got %q", drv.Name())
	}

	// Type
	if drv.Type() != "modbus-tcp" {
		t.Errorf("expected type 'modbus-tcp', got %q", drv.Type())
	}

	// Status
	status := drv.Status()
	if status.Name != "test-plc" {
		t.Errorf("expected status name 'test-plc', got %q", status.Name)
	}
	if status.Type != "modbus-tcp" {
		t.Errorf("expected status type 'modbus-tcp', got %q", status.Type)
	}
	if status.State != core.StateConnected {
		t.Errorf("expected connected state, got %v", status.State)
	}
	if status.TagCount != 1 {
		t.Errorf("expected tag count 1, got %d", status.TagCount)
	}

	// Subscribe — Modbus doesn't support subscribe
	ch, err := drv.Subscribe(ctx, []string{"temp"})
	if err != core.ErrSubscribeNotSupported {
		t.Errorf("expected ErrSubscribeNotSupported, got %v", err)
	}
	if ch != nil {
		t.Error("expected nil channel")
	}
}

// --- Restart test ---

func TestModbusRestart(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
	}

	handler := &testModbusHandler{
		coils:          map[uint16]bool{},
		holding:        map[uint16]uint16{0: 42},
		discreteInputs: map[uint16]bool{},
		inputRegs:      map[uint16]uint16{},
	}
	server, err := mb.NewServer(&mb.ServerConfiguration{
		URL:     fmt.Sprintf("tcp://127.0.0.1:%d", port),
		Timeout: 5 * time.Second,
	}, handler)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer server.Stop()
	// server.Start() is synchronous — ready immediately.

	cfg := core.DriverConfig{
		Name: "restart-plc",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":     "127.0.0.1",
			"port":     port,
			"slave-id": 1,
			"timeout":  "2s",
		},
		Tags: []core.TagConfig{
			{Name: "val", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusTCPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}
	ctx := context.Background()
	if err := drv.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := drv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer drv.Stop()

	// Verify initial connection.
	vals, err := drv.Read(ctx, []string{"val"})
	if err != nil {
		t.Fatalf("Read before restart: %v", err)
	}
	if vals[0].Value != uint16(42) {
		t.Errorf("expected 42, got %v", vals[0].Value)
	}

	// Restart with same config.
	if err := drv.Restart(ctx, cfg); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	// Poll until reconnected after restart (replaces fixed sleep).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if drv.Status().State == core.StateConnected {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	vals, err = drv.Read(ctx, []string{"val"})
	if err != nil {
		t.Fatalf("Read after restart: %v", err)
	}
	if vals[0].Value != uint16(42) {
		t.Errorf("expected 42 after restart, got %v", vals[0].Value)
	}
}

// --- Reconnect test (connect to non-existent server, then start it) ---

func TestModbusReconnectOnFailure(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
	}

	cfg := core.DriverConfig{
		Name: "reconnect-plc",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":               "127.0.0.1",
			"port":               port,
			"slave-id":           1,
			"timeout":            "500ms",
			"retry-interval":     "100ms",
			"max-retry-interval": "200ms",
		},
		Tags: []core.TagConfig{
			{Name: "val", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusTCPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}
	ctx := context.Background()
	if err := drv.Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Start — server is not running, so initial connect fails.
	// Start should NOT return an error; it should schedule reconnect.
	if err := drv.Start(ctx); err != nil {
		t.Fatalf("Start should not fail on connect error: %v", err)
	}
	defer drv.Stop()

	// Verify state is "connecting" (reconnecting).
	status := drv.Status()
	if status.State != core.StateConnecting && status.State != core.StateError {
		t.Errorf("expected connecting/error state, got %v", status.State)
	}

	// Now start the server — the reconnect loop should connect.
	handler := &testModbusHandler{
		coils:          map[uint16]bool{},
		holding:        map[uint16]uint16{0: 77},
		discreteInputs: map[uint16]bool{},
		inputRegs:      map[uint16]uint16{},
	}
	server, err := mb.NewServer(&mb.ServerConfiguration{
		URL:     fmt.Sprintf("tcp://127.0.0.1:%d", port),
		Timeout: 5 * time.Second,
	}, handler)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer server.Stop()

	// Wait for reconnect.
	connected := false
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if drv.Status().State == core.StateConnected {
			connected = true
			break
		}
	}
	if !connected {
		t.Error("driver did not reconnect after server started")
	}

	// Verify we can read.
	if connected {
		vals, err := drv.Read(ctx, []string{"val"})
		if err != nil {
			t.Fatalf("Read after reconnect: %v", err)
		}
		if vals[0].Value != uint16(77) {
			t.Errorf("expected 77, got %v", vals[0].Value)
		}
	}
}

// --- Capabilities test ---

func TestModbusCapabilities(t *testing.T) {
	drv, err := NewModbusTCPDriver(core.DriverConfig{
		Name: "cap-test",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host": "127.0.0.1",
			"port": 502,
		},
		Tags: []core.TagConfig{{Name: "t", Address: "40001", Type: "uint16"}},
	})
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}

	caps := drv.Capabilities()
	if !caps.CanRead || !caps.CanWrite {
		t.Error("expected CanRead and CanWrite to be true")
	}
	if caps.CanSubscribe {
		t.Error("expected CanSubscribe to be false")
	}
	if !caps.BatchRead {
		t.Error("expected BatchRead to be true")
	}
	if caps.MaxBatchSize != 125 {
		t.Errorf("expected MaxBatchSize 125, got %d", caps.MaxBatchSize)
	}
}

// --- Init error paths ---

func TestModbusInitMissingHost(t *testing.T) {
	drv, err := NewModbusTCPDriver(core.DriverConfig{
		Name: "no-host",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"port": 502,
		},
		Tags: []core.TagConfig{{Name: "t", Address: "40001", Type: "uint16"}},
	})
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}

	err = drv.Init(context.Background(), core.DriverConfig{
		Name: "no-host",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"port": 502,
		},
		Tags: []core.TagConfig{{Name: "t", Address: "40001", Type: "uint16"}},
	})
	if err == nil {
		t.Error("expected error for missing host")
	}
}

func TestModbusInitInvalidAddress(t *testing.T) {
	drv, err := NewModbusTCPDriver(core.DriverConfig{
		Name: "bad-addr",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host": "127.0.0.1",
			"port": 502,
		},
		Tags: []core.TagConfig{{Name: "t", Address: "invalid", Type: "uint16"}},
	})
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}

	err = drv.Init(context.Background(), core.DriverConfig{
		Name: "bad-addr",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host": "127.0.0.1",
			"port": 502,
		},
		Tags: []core.TagConfig{{Name: "t", Address: "invalid", Type: "uint16"}},
	})
	if err == nil {
		t.Error("expected error for invalid address")
	}
}

// --- Stop without Start ---

func TestModbusStopWithoutStart(t *testing.T) {
	drv, err := NewModbusTCPDriver(core.DriverConfig{
		Name: "stop-test",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host": "127.0.0.1",
			"port": 502,
		},
		Tags: []core.TagConfig{{Name: "t", Address: "40001", Type: "uint16"}},
	})
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}

	if err := drv.Init(context.Background(), core.DriverConfig{
		Name: "stop-test",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host": "127.0.0.1",
			"port": 502,
		},
		Tags: []core.TagConfig{{Name: "t", Address: "40001", Type: "uint16"}},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Stop without Start should not panic.
	if err := drv.Stop(); err != nil {
		t.Errorf("Stop without Start failed: %v", err)
	}
}
