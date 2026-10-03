// Package modbus implements the Modbus TCP and RTU protocol drivers for CoreC.
//
// This file holds tests for the ReconnectCount status field, which is
// populated from the reconnect loop's counted variant and surfaced via
// Status() so it can be exposed as a Prometheus metric.
package modbus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/common/testutil"
	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

// TestModbusReconnectCountStartsAtZero verifies that a freshly-constructed
// driver reports ReconnectCount == 0 via Status() before any reconnect
// attempt has been made.
func TestModbusReconnectCountStartsAtZero(t *testing.T) {
	drv, err := NewModbusTCPDriver(core.DriverConfig{
		Name: "rc-zero",
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

	st := drv.Status()
	if st.ReconnectCount != 0 {
		t.Errorf("expected ReconnectCount 0 on a fresh driver, got %d", st.ReconnectCount)
	}
}

// TestModbusReconnectCountIncrements verifies that ReconnectCount increases
// as the background reconnect loop makes attempts. The driver is pointed at
// a port with no server, so every connect attempt fails and the loop keeps
// retrying; ReconnectCount must grow monotonically while the loop runs.
func TestModbusReconnectCountIncrements(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
	}

	cfg := core.DriverConfig{
		Name: "rc-incr",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":                   "127.0.0.1",
			"port":                   port,
			"slave-id":               1,
			"timeout":                "200ms",
			"reconnect-interval":     "20ms",
			"reconnect-max-interval": "40ms",
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
		t.Fatalf("Start should not fail on connect error: %v", err)
	}
	defer drv.Stop()

	first := testutil.PollReconnectCount(t, drv, 3*time.Second)
	testutil.PollReconnectCountGrowing(t, drv, first, 2*time.Second)
}

// TestModbusReconnectCountOnRecovery verifies the full reconnect story
// end-to-end: with the server down, the counter climbs; once the server
// comes up, the driver reconnects and the counter records the successful
// attempt too. After a successful reconnect the counter must not keep
// growing (the loop exits on success).
func TestModbusReconnectCountOnRecovery(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
	}

	cfg := core.DriverConfig{
		Name: "rc-recover",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":                   "127.0.0.1",
			"port":                   port,
			"slave-id":               1,
			"timeout":                "500ms",
			"reconnect-interval":     "50ms",
			"reconnect-max-interval": "100ms",
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

	// Wait for the reconnect loop to make at least two attempts.
	// PollReconnectCount returns as soon as the counter exceeds zero,
	// but the attempt that incremented it may still be in progress
	// (has not yet called connect()). If the server starts during
	// that window, the in-progress attempt succeeds and the counter
	// does not advance further, causing countAtReconnect == countBeforeServer.
	//
	// PollReconnectCountGrowing waits for the counter to advance past
	// firstCount, proving that attempt completed (failed). Using
	// firstCount as countBeforeServer is safe because the counter is
	// monotonic — any later reading, including after the server starts
	// and the driver reconnects, will be strictly greater.
	firstCount := testutil.PollReconnectCount(t, drv, 3*time.Second)
	testutil.PollReconnectCountGrowing(t, drv, firstCount, 3*time.Second)
	countBeforeServer := firstCount

	// Now bring the server up. The reconnect loop should succeed and the
	// counter must record that final successful attempt.
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

	if !testutil.PollUntilConnected(t, drv, 5*time.Second) {
		t.Fatal("driver did not reconnect after server started")
	}

	// After a successful reconnect, the counter must have advanced (the
	// successful attempt was also counted) and must then stay stable — the
	// loop exits on success, so no further attempts are made.
	countAtReconnect := drv.Status().ReconnectCount
	if countAtReconnect <= countBeforeServer {
		t.Errorf("expected ReconnectCount to advance on successful reconnect, before=%d atReconnect=%d",
			countBeforeServer, countAtReconnect)
	}

	time.Sleep(150 * time.Millisecond)
	if got := drv.Status().ReconnectCount; got != countAtReconnect {
		t.Errorf("ReconnectCount changed after successful reconnect (loop should have exited): was=%d now=%d",
			countAtReconnect, got)
	}
}
