package modbus

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

type testModbusHandler struct {
	coils          map[uint16]bool
	discreteInputs map[uint16]bool
	holding        map[uint16]uint16
	inputRegs      map[uint16]uint16
}

func (h *testModbusHandler) HandleCoils(req *mb.CoilsRequest) ([]bool, error) {
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

func (h *testModbusHandler) HandleDiscreteInputs(req *mb.DiscreteInputsRequest) ([]bool, error) {
	res := make([]bool, req.Quantity)
	for i := uint16(0); i < req.Quantity; i++ {
		res[i] = h.discreteInputs[req.Addr+i]
	}
	return res, nil
}

func (h *testModbusHandler) HandleHoldingRegisters(req *mb.HoldingRegistersRequest) ([]uint16, error) {
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

func (h *testModbusHandler) HandleInputRegisters(req *mb.InputRegistersRequest) ([]uint16, error) {
	res := make([]uint16, req.Quantity)
	for i := uint16(0); i < req.Quantity; i++ {
		res[i] = h.inputRegs[req.Addr+i]
	}
	return res, nil
}

func getFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func TestModbusAddressParsing(t *testing.T) {
	tests := []struct {
		addr     string
		expected areaType
		regAddr  uint16
	}{
		{"00001", areaCoil, 0},
		{"10001", areaDiscreteInput, 0},
		{"30001", areaInputRegister, 0},
		{"30010", areaInputRegister, 9},
		{"40001", areaHoldingRegister, 0},
		{"40100", areaHoldingRegister, 99},
	}

	for _, tt := range tests {
		ai, err := parseModbusAddress(tt.addr)
		if err != nil {
			t.Fatalf("unexpected error parsing %s: %v", tt.addr, err)
		}
		if ai.area != tt.expected {
			t.Errorf("addr %s: expected area %v, got %v", tt.addr, tt.expected, ai.area)
		}
		if ai.addr != tt.regAddr {
			t.Errorf("addr %s: expected regAddr %d, got %d", tt.addr, tt.regAddr, ai.addr)
		}
	}
}

func TestModbusLoopbackReadWrite(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}

	handler := &testModbusHandler{
		coils:          map[uint16]bool{0: true, 1: false},
		discreteInputs: map[uint16]bool{0: true, 1: true, 2: false},
		holding:        map[uint16]uint16{0: 1234, 1: 5678},
		inputRegs:      map[uint16]uint16{0: 999},
	}

	server, err := mb.NewServer(&mb.ServerConfiguration{
		URL:     fmt.Sprintf("tcp://127.0.0.1:%d", port),
		Timeout: 5 * time.Second,
	}, handler)
	if err != nil {
		t.Fatalf("failed to create modbus server: %v", err)
	}

	if err := server.Start(); err != nil {
		t.Fatalf("failed to start modbus server: %v", err)
	}
	defer server.Stop()
	// server.Start() is synchronous — ready immediately.

	driverCfg := core.DriverConfig{
		Name: "test-plc",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":     "127.0.0.1",
			"port":     port,
			"slave-id": 1,
			"timeout":  "2s",
		},
		Tags: []core.TagConfig{
			{Name: "pump_on", Address: "00001", Type: "bool"},
			{Name: "temp", Address: "40001", Type: "uint16"},
			{Name: "speed", Address: "30001", Type: "uint16"},
			{Name: "fault_flag", Address: "10001", Type: "bool"},
		},
	}

	drv, err := NewModbusTCPDriver(driverCfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver error: %v", err)
	}

	ctx := context.Background()
	if err := drv.Init(ctx, driverCfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}

	if err := drv.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer drv.Stop()

	// Test reading
	values, err := drv.Read(ctx, []string{"pump_on", "temp", "speed", "fault_flag"})
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}

	if len(values) != 4 {
		t.Fatalf("expected 4 values, got %d", len(values))
	}

	if values[0].Value != true {
		t.Errorf("pump_on: expected true, got %v", values[0].Value)
	}
	if values[1].Value != uint16(1234) {
		t.Errorf("temp: expected 1234, got %v", values[1].Value)
	}
	if values[2].Value != uint16(999) {
		t.Errorf("speed: expected 999, got %v", values[2].Value)
	}
	// fault_flag is a discrete input (1xxxx / FC02) — verifies the
	// discreteInputs map is actually read, not the coils map.
	if values[3].Value != true {
		t.Errorf("fault_flag: expected true, got %v", values[3].Value)
	}

	// Test writing
	writeResults, err := drv.Write(ctx, []core.WriteCommand{
		{Driver: "test-plc", Tag: "temp", Value: uint16(4321), Type: core.TypeUint16},
		{Driver: "test-plc", Tag: "pump_on", Value: false, Type: core.TypeBool},
	})
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	for i, wr := range writeResults {
		if !wr.Success {
			t.Errorf("write %d failed: %v", i, wr.Error)
		}
	}

	// Verify write results on server
	if handler.holding[0] != 4321 {
		t.Errorf("expected holding[0] = 4321, got %d", handler.holding[0])
	}
	if handler.coils[0] != false {
		t.Errorf("expected coils[0] = false, got %v", handler.coils[0])
	}
}

// TestModbusDiscreteInputsRead specifically exercises the FC02 discrete-input
// (1xxxx) area end-to-end through a real loopback modbus server. This guards
// against regressions where HandleDiscreteInputs accidentally reads the coils
// map instead of a dedicated discrete-inputs map.
func TestModbusDiscreteInputsRead(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}

	handler := &testModbusHandler{
		coils:          map[uint16]bool{0: false, 1: false, 2: false},
		discreteInputs: map[uint16]bool{0: true, 1: false, 2: true},
		holding:        map[uint16]uint16{},
		inputRegs:      map[uint16]uint16{},
	}

	server, err := mb.NewServer(&mb.ServerConfiguration{
		URL:     fmt.Sprintf("tcp://127.0.0.1:%d", port),
		Timeout: 5 * time.Second,
	}, handler)
	if err != nil {
		t.Fatalf("failed to create modbus server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start modbus server: %v", err)
	}
	defer server.Stop()
	// server.Start() is synchronous — ready immediately.

	driverCfg := core.DriverConfig{
		Name: "test-plc-di",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":     "127.0.0.1",
			"port":     port,
			"slave-id": 1,
			"timeout":  "2s",
		},
		Tags: []core.TagConfig{
			{Name: "di0", Address: "10001", Type: "bool"}, // addr 0
			{Name: "di1", Address: "10002", Type: "bool"}, // addr 1
			{Name: "di2", Address: "10003", Type: "bool"}, // addr 2
		},
	}

	drv, err := NewModbusTCPDriver(driverCfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver error: %v", err)
	}
	ctx := context.Background()
	if err := drv.Init(ctx, driverCfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}
	if err := drv.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer drv.Stop()

	values, err := drv.Read(ctx, []string{"di0", "di1", "di2"})
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(values) != 3 {
		t.Fatalf("expected 3 values, got %d", len(values))
	}

	expected := []bool{true, false, true}
	for i, want := range expected {
		if values[i].Value != want {
			t.Errorf("di%d: expected %v, got %v", i, want, values[i].Value)
		}
	}
}

// TestModbusNonBoolOnBitArea verifies that configuring a non-bool type on a
// coil (0xxxx) or discrete-input (1xxxx) address returns an explicit error on
// both read and write, instead of silently routing the request to a holding
// register and reading/writing the wrong memory area (H12).
func TestModbusNonBoolOnBitArea(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}

	handler := &testModbusHandler{
		coils:          map[uint16]bool{0: true},
		discreteInputs: map[uint16]bool{0: true},
		holding:        map[uint16]uint16{0: 42},
		inputRegs:      map[uint16]uint16{},
	}

	server, err := mb.NewServer(&mb.ServerConfiguration{
		URL:     fmt.Sprintf("tcp://127.0.0.1:%d", port),
		Timeout: 5 * time.Second,
	}, handler)
	if err != nil {
		t.Fatalf("failed to create modbus server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start modbus server: %v", err)
	}
	defer server.Stop()

	driverCfg := core.DriverConfig{
		Name: "test-plc-bitarea",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":     "127.0.0.1",
			"port":     port,
			"slave-id": 1,
			"timeout":  "2s",
		},
		Tags: []core.TagConfig{
			{Name: "coil_u16", Address: "00001", Type: "uint16"}, // coil (0xxxx), non-bool
			{Name: "di_u16", Address: "10001", Type: "uint16"},   // discrete input (1xxxx), non-bool
		},
	}

	drv, err := NewModbusTCPDriver(driverCfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver error: %v", err)
	}
	ctx := context.Background()
	if err := drv.Init(ctx, driverCfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}
	if err := drv.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer drv.Stop()

	// Reading a non-bool type on a bit area must fail explicitly rather than
	// silently returning data from the holding-register area.
	values, err := drv.Read(ctx, []string{"coil_u16", "di_u16"})
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(values))
	}
	for _, v := range values {
		if v.Quality != core.QualityBad {
			t.Errorf("tag %s: expected QualityBad, got %v", v.Tag, v.Quality)
		}
		if v.Error == nil {
			t.Errorf("tag %s: expected error, got nil", v.Tag)
			continue
		}
		if !strings.Contains(v.Error.Error(), "not supported") {
			t.Errorf("tag %s: expected 'not supported' error, got: %v", v.Tag, v.Error)
		}
	}

	// Writing a non-bool type on a coil area must also fail explicitly.
	writeResults, err := drv.Write(ctx, []core.WriteCommand{
		{Driver: "test-plc-bitarea", Tag: "coil_u16", Value: uint16(1), Type: core.TypeUint16},
	})
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if len(writeResults) != 1 {
		t.Fatalf("expected 1 write result, got %d", len(writeResults))
	}
	if writeResults[0].Success {
		t.Errorf("expected write to fail, got success")
	}
	if !strings.Contains(writeResults[0].Error, "not supported") {
		t.Errorf("expected 'not supported' error, got: %v", writeResults[0].Error)
	}
}
