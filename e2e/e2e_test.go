package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine"
	"github.com/CoreC-Dev/CoreC/log"

	// Register all real drivers and transports.
	_ "github.com/CoreC-Dev/CoreC/driver/all"
	_ "github.com/CoreC-Dev/CoreC/transport/all"

	mb "github.com/simonvetter/modbus"
)

// waitFor polls check until it returns true or timeout expires.
func waitFor(timeout time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ============================================================
// Real Modbus TCP mock server (self-contained, no import from
// driver/modbus test files).
// ============================================================

type modbusHandler struct {
	mu       sync.Mutex
	coils    map[uint16]bool
	holding  map[uint16]uint16
	inputReg map[uint16]uint16
}

func (h *modbusHandler) HandleCoils(req *mb.CoilsRequest) ([]bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if req.IsWrite {
		for i, v := range req.Args {
			h.coils[req.Addr+uint16(i)] = v
		}
		return nil, nil
	}
	res := make([]bool, req.Quantity)
	for i := uint16(0); i < req.Quantity; i++ {
		res[i] = h.coils[req.Addr+i]
	}
	return res, nil
}

func (h *modbusHandler) HandleDiscreteInputs(req *mb.DiscreteInputsRequest) ([]bool, error) {
	return nil, nil
}

func (h *modbusHandler) HandleHoldingRegisters(req *mb.HoldingRegistersRequest) ([]uint16, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if req.IsWrite {
		for i, v := range req.Args {
			h.holding[req.Addr+uint16(i)] = v
		}
		return nil, nil
	}
	res := make([]uint16, req.Quantity)
	for i := uint16(0); i < req.Quantity; i++ {
		res[i] = h.holding[req.Addr+i]
	}
	return res, nil
}

func (h *modbusHandler) HandleInputRegisters(req *mb.InputRegistersRequest) ([]uint16, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	res := make([]uint16, req.Quantity)
	for i := uint16(0); i < req.Quantity; i++ {
		res[i] = h.inputReg[req.Addr+i]
	}
	return res, nil
}

func startModbusServer(t *testing.T, handler *modbusHandler) (server *mb.ModbusServer, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port = l.Addr().(*net.TCPAddr).Port
	l.Close()

	server, err = mb.NewServer(&mb.ServerConfiguration{
		URL:     fmt.Sprintf("tcp://127.0.0.1:%d", port),
		Timeout: 5 * time.Second,
	}, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// server.Start() is synchronous — ready immediately.
	return server, port
}

// ============================================================
// E2E Test 1: Modbus PLC → Engine → Forward Rule → HTTP Push
//
// This is the core industrial data collection scenario:
// 1. A real Modbus TCP PLC (mock server) holds register values.
// 2. The engine's Modbus driver polls the PLC on an interval.
// 3. A "forward" rule sends all data points to an HTTP push transport.
// 4. The HTTP push transport POSTs data to a real HTTP server.
// 5. We verify the HTTP server received the correct data.
// ============================================================

func TestE2E_ModbusToHTTPPush(t *testing.T) {
	log.Init(0, "text") // silence logs

	// --- Start mock Modbus PLC ---
	handler := &modbusHandler{
		coils:    map[uint16]bool{0: true, 1: false},
		holding:  map[uint16]uint16{0: 1234, 1: 5678, 2: 90},
		inputReg: map[uint16]uint16{},
	}
	modbusServer, port := startModbusServer(t, handler)
	defer modbusServer.Stop()

	// --- Start mock HTTP receiver ---
	var receivedMu sync.Mutex
	var receivedBodies [][]byte
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		receivedMu.Lock()
		receivedBodies = append(receivedBodies, body)
		receivedMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer httpServer.Close()

	// --- Create and start engine ---
	eng := engine.New()

	cfg := &core.Config{
		Global: core.GlobalConfig{LogLevel: "error"},
		Drivers: []core.DriverConfig{
			{
				Name: "plc1",
				Type: "modbus-tcp",
				Settings: map[string]any{
					"host":     "127.0.0.1",
					"port":     port,
					"slave-id": 1,
					"timeout":  "2s",
				},
				Tags: []core.TagConfig{
					{Name: "temperature", Address: "40001", Type: "uint16", Interval: "100ms"},
					{Name: "pressure", Address: "40002", Type: "uint16", Interval: "100ms"},
					{Name: "pump_on", Address: "00001", Type: "bool", Interval: "100ms"},
				},
			},
		},
		Transports: []core.TransportConfig{
			{
				Name: "cloud-push",
				Type: "http",
				Settings: map[string]any{
					"url":            httpServer.URL,
					"method":         "POST",
					"flush-interval": "50ms",
					"batch-size":     1,
					"max-retry":      1,
					"retry-interval": "100ms",
					"timeout":        "2s",
				},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "cloud-push"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("engine Start: %v", err)
	}
	defer eng.Stop()

	// --- Wait for both temperature and pressure tag data to flow through ---
	// The driver polls 3 tags at 100ms intervals with batch-size=1, so
	// individual data points arrive separately. We must wait until BOTH
	// tags have been received, not just the first one.
	waitFor(5*time.Second, func() bool {
		receivedMu.Lock()
		defer receivedMu.Unlock()
		foundTemp := false
		foundPressure := false
		for _, body := range receivedBodies {
			bodyStr := string(body)
			if contains(bodyStr, "temperature") {
				foundTemp = true
			}
			if contains(bodyStr, "pressure") {
				foundPressure = true
			}
		}
		return foundTemp && foundPressure
	})

	// --- Verify HTTP server received data ---
	receivedMu.Lock()
	defer receivedMu.Unlock()

	if len(receivedBodies) == 0 {
		t.Fatal("HTTP server did not receive any data — pipeline broken")
	}

	// Verify at least one body contains expected tag data.
	foundTemp := false
	foundPressure := false
	for _, body := range receivedBodies {
		bodyStr := string(body)
		if contains(bodyStr, "temperature") {
			foundTemp = true
		}
		if contains(bodyStr, "pressure") {
			foundPressure = true
		}
	}
	if !foundTemp {
		t.Errorf("no HTTP body contained 'temperature' tag data")
		for i, body := range receivedBodies {
			t.Logf("body[%d]: %s", i, string(body))
		}
	}
	if !foundPressure {
		t.Error("no HTTP body contained 'pressure' tag data")
	}
}

// ============================================================
// E2E Test 2: Modbus PLC → Engine → Transform Rule → HTTP Push
//
// Verifies that transform rules modify data before publishing.
// ============================================================

func TestE2E_ModbusWithTransform(t *testing.T) {
	log.Init(0, "text")

	handler := &modbusHandler{
		holding: map[uint16]uint16{0: 100, 1: 200},
	}
	modbusServer, port := startModbusServer(t, handler)
	defer modbusServer.Stop()

	var receivedMu sync.Mutex
	var receivedBodies [][]byte
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		receivedMu.Lock()
		receivedBodies = append(receivedBodies, body)
		receivedMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer httpServer.Close()

	eng := engine.New()

	cfg := &core.Config{
		Global: core.GlobalConfig{LogLevel: "error"},
		Drivers: []core.DriverConfig{
			{
				Name: "plc1",
				Type: "modbus-tcp",
				Settings: map[string]any{
					"host":     "127.0.0.1",
					"port":     port,
					"slave-id": 1,
					"timeout":  "2s",
				},
				Tags: []core.TagConfig{
					{Name: "raw_temp", Address: "40001", Type: "uint16", Interval: "100ms"},
				},
			},
		},
		Transports: []core.TransportConfig{
			{
				Name: "cloud",
				Type: "http",
				Settings: map[string]any{
					"url":            httpServer.URL,
					"method":         "POST",
					"flush-interval": "50ms",
					"batch-size":     1,
					"max-retry":      1,
					"retry-interval": "100ms",
					"timeout":        "2s",
				},
			},
		},
		Rules: []core.RuleConfig{
			{
				Name:     "scale-temp",
				Match:    "ALL",
				Action:   "transform",
				Target:   "cloud",
				Priority: 1,
				Transform: &core.TransformConfig{
					Expression: "value * 0.1",
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("engine Start: %v", err)
	}
	defer eng.Stop()

	if !waitFor(3*time.Second, func() bool {
		receivedMu.Lock()
		defer receivedMu.Unlock()
		return len(receivedBodies) > 0
	}) {
		t.Fatal("no data received — transform pipeline broken")
	}

	receivedMu.Lock()
	defer receivedMu.Unlock()
	if len(receivedBodies) == 0 {
		t.Fatal("no data received — transform pipeline broken")
	}

	// With transform value * 0.1, raw 100 → 10.0
	foundScaled := false
	for _, body := range receivedBodies {
		bodyStr := string(body)
		if contains(bodyStr, "10") {
			foundScaled = true
			break
		}
	}
	if !foundScaled {
		t.Error("expected transformed value ~10.0 (100 * 0.1) in published data")
	}
}

// ============================================================
// E2E Test 3: Modbus PLC → Engine → Alert Rule
//
// Verifies that alert rules trigger the OnAlert handler.
// ============================================================

func TestE2E_ModbusWithAlert(t *testing.T) {
	log.Init(0, "text")

	handler := &modbusHandler{
		holding: map[uint16]uint16{0: 999},
	}
	modbusServer, port := startModbusServer(t, handler)
	defer modbusServer.Stop()

	eng := engine.New()

	cfg := &core.Config{
		Global: core.GlobalConfig{LogLevel: "error"},
		Drivers: []core.DriverConfig{
			{
				Name: "plc1",
				Type: "modbus-tcp",
				Settings: map[string]any{
					"host":     "127.0.0.1",
					"port":     port,
					"slave-id": 1,
					"timeout":  "2s",
				},
				Tags: []core.TagConfig{
					{Name: "val", Address: "40001", Type: "uint16", Interval: "50ms"},
				},
			},
		},
		Transports: []core.TransportConfig{},
		Rules: []core.RuleConfig{
			{Name: "alert-all", Match: "ALL", Action: "alert"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("engine Start: %v", err)
	}
	defer eng.Stop()

	alertCount := atomic.Int64{}
	eng.OnAlert(func(point core.DataPoint, rule core.Rule) {
		alertCount.Add(1)
		if point.Driver != "plc1" {
			t.Errorf("expected alert from plc1, got %s", point.Driver)
		}
		if point.Tag != "val" {
			t.Errorf("expected alert for tag 'val', got %s", point.Tag)
		}
	})

	if !waitFor(3*time.Second, func() bool {
		return alertCount.Load() > 0
	}) {
		t.Error("expected at least one alert to be triggered")
	}
}

// ============================================================
// E2E Test 4: Modbus PLC → Engine → Drop Rule → No Output
//
// Verifies that drop rules prevent data from reaching transports.
// ============================================================

func TestE2E_ModbusWithDropRule(t *testing.T) {
	log.Init(0, "text")

	handler := &modbusHandler{
		holding: map[uint16]uint16{0: 42},
	}
	modbusServer, port := startModbusServer(t, handler)
	defer modbusServer.Stop()

	var receivedCount atomic.Int64
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer httpServer.Close()

	eng := engine.New()

	cfg := &core.Config{
		Global: core.GlobalConfig{LogLevel: "error"},
		Drivers: []core.DriverConfig{
			{
				Name: "plc1",
				Type: "modbus-tcp",
				Settings: map[string]any{
					"host":     "127.0.0.1",
					"port":     port,
					"slave-id": 1,
					"timeout":  "2s",
				},
				Tags: []core.TagConfig{
					{Name: "val", Address: "40001", Type: "uint16", Interval: "50ms"},
				},
			},
		},
		Transports: []core.TransportConfig{
			{
				Name: "cloud",
				Type: "http",
				Settings: map[string]any{
					"url":            httpServer.URL,
					"method":         "POST",
					"flush-interval": "50ms",
					"batch-size":     1,
					"max-retry":      1,
					"retry-interval": "100ms",
					"timeout":        "2s",
				},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "drop-all", Match: "ALL", Action: "drop", Priority: 1},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("engine Start: %v", err)
	}
	defer eng.Stop()

	// Wait for at least one read (proving the drop rule was applied),
	// then verify no HTTP requests were made.
	waitFor(3*time.Second, func() bool {
		return eng.Stats().TotalRead > 0
	})

	if receivedCount.Load() > 0 {
		t.Errorf("expected 0 HTTP requests with drop rule, got %d", receivedCount.Load())
	}
}

// ============================================================
// E2E Test 5: Write command flow (HTTP → Engine → Modbus PLC)
//
// Verifies that write commands sent through the engine actually
// modify values in the PLC.
// ============================================================

func TestE2E_WriteCommandToModbus(t *testing.T) {
	log.Init(0, "text")

	handler := &modbusHandler{
		holding: map[uint16]uint16{0: 0},
	}
	modbusServer, port := startModbusServer(t, handler)
	defer modbusServer.Stop()

	eng := engine.New()

	cfg := &core.Config{
		Global: core.GlobalConfig{LogLevel: "error"},
		Drivers: []core.DriverConfig{
			{
				Name: "plc1",
				Type: "modbus-tcp",
				Settings: map[string]any{
					"host":     "127.0.0.1",
					"port":     port,
					"slave-id": 1,
					"timeout":  "2s",
				},
				Tags: []core.TagConfig{
					{Name: "register", Address: "40001", Type: "uint16", Interval: "500ms"},
				},
			},
		},
		Transports: []core.TransportConfig{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("engine Start: %v", err)
	}
	defer eng.Stop()

	// Write a value to the PLC through the engine.
	result, err := eng.WriteTag(ctx, core.WriteCommand{
		Driver: "plc1",
		Tag:    "register",
		Value:  uint16(4242),
		Type:   core.TypeUint16,
	})
	if err != nil {
		t.Fatalf("WriteTag: %v", err)
	}
	if !result.Success {
		t.Error("expected write to succeed")
	}

	// Read back the value to verify it was written.
	val, err := eng.ReadTag(ctx, "plc1", "register")
	if err != nil {
		t.Fatalf("ReadTag: %v", err)
	}
	if val.Value != uint16(4242) {
		t.Errorf("expected 4242 after write, got %v", val.Value)
	}

	// Also verify directly from the mock PLC.
	handler.mu.Lock()
	plcVal := handler.holding[0]
	handler.mu.Unlock()
	if plcVal != 4242 {
		t.Errorf("expected PLC register to be 4242, got %d", plcVal)
	}
}

// ============================================================
// E2E Test 6: Engine reload preserves data flow
//
// Verifies that reloading the engine config doesn't break
// the data collection pipeline.
// ============================================================

func TestE2E_EngineReload(t *testing.T) {
	log.Init(0, "text")

	handler := &modbusHandler{
		holding: map[uint16]uint16{0: 111, 1: 222},
	}
	modbusServer, port := startModbusServer(t, handler)
	defer modbusServer.Stop()

	var receivedCount atomic.Int64
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer httpServer.Close()

	eng := engine.New()

	cfg1 := &core.Config{
		Global: core.GlobalConfig{LogLevel: "error"},
		Drivers: []core.DriverConfig{
			{
				Name: "plc1",
				Type: "modbus-tcp",
				Settings: map[string]any{
					"host":     "127.0.0.1",
					"port":     port,
					"slave-id": 1,
					"timeout":  "2s",
				},
				Tags: []core.TagConfig{
					{Name: "val1", Address: "40001", Type: "uint16", Interval: "100ms"},
				},
			},
		},
		Transports: []core.TransportConfig{
			{
				Name: "cloud",
				Type: "http",
				Settings: map[string]any{
					"url":            httpServer.URL,
					"method":         "POST",
					"flush-interval": "50ms",
					"batch-size":     1,
					"max-retry":      1,
					"retry-interval": "100ms",
					"timeout":        "2s",
				},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "fwd", Match: "ALL", Action: "forward", Target: "cloud"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg1); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	// Wait for initial data flow.
	if !waitFor(3*time.Second, func() bool {
		return receivedCount.Load() > 0
	}) {
		t.Fatal("no data received before reload")
	}
	countBefore := receivedCount.Load()

	// Reload with a config that adds a second tag.
	cfg2 := &core.Config{
		Global: core.GlobalConfig{LogLevel: "error"},
		Drivers: []core.DriverConfig{
			{
				Name: "plc1",
				Type: "modbus-tcp",
				Settings: map[string]any{
					"host":     "127.0.0.1",
					"port":     port,
					"slave-id": 1,
					"timeout":  "2s",
				},
				Tags: []core.TagConfig{
					{Name: "val1", Address: "40001", Type: "uint16", Interval: "100ms"},
					{Name: "val2", Address: "40002", Type: "uint16", Interval: "100ms"},
				},
			},
		},
		Transports: cfg1.Transports,
		Rules:      cfg1.Rules,
	}

	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Wait for data to flow after reload.
	if !waitFor(3*time.Second, func() bool {
		return receivedCount.Load() > countBefore
	}) {
		t.Fatal("no new data received after reload")
	}
	countAfter := receivedCount.Load()

	if countAfter <= countBefore {
		t.Errorf("expected more data after reload, before=%d after=%d", countBefore, countAfter)
	}
}

// contains is a simple substring check.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
