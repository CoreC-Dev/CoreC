package modbus

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// approxEqual compares two readTag return values, using a tolerance for
// floating-point types where exact bit equality is fragile across the
// encode/decode round trip.
func approxEqual(a, b any) bool {
	switch bv := b.(type) {
	case float32:
		av, ok := a.(float32)
		return ok && math.Abs(float64(av-bv)) < 1e-4
	case float64:
		av, ok := a.(float64)
		return ok && math.Abs(av-bv) < 1e-9
	default:
		return a == b
	}
}

// connectedClient starts a loopback Modbus server with the given handler,
// builds a connected TCP driver, and returns the driver plus the live client
// for direct readTag calls. The caller defers the returned cleanup func.
func connectedClient(t *testing.T, handler *testModbusHandler) (d *ModbusTCPDriver, cleanup func()) {
	t.Helper()
	server, port := startTestServer(t, handler)
	drv := newTCPDriver(t, port, []core.TagConfig{
		{Name: "dummy", Address: "40001", Type: "uint16"},
	})
	d = drv.(*ModbusTCPDriver)
	d.RLock()
	client := d.client
	d.RUnlock()
	if client == nil {
		t.Fatal("expected non-nil client after Start")
	}
	cleanup = func() {
		_ = drv.Stop()
		_ = server.Stop()
	}
	return
}

// TestModbusReadTagAllTypes exercises every data-type branch of readTag
// against a live loopback server, verifying that values round-trip
// correctly for coils, discrete inputs, input registers, and holding
// registers across all supported scalar types.
func TestModbusReadTagAllTypes(t *testing.T) {
	holding := map[uint16]uint16{
		0: 0x1234,
		1: 0xAABB, 2: 0xCCDD, // uint32 0xAABBCCDD
		3: 0xFFFF, 4: 0xCFC7, // int32 -12345
		19: 0xFFD6, // int16 -42
		20: 1,      // bool true
		21: 0,      // bool false
	}
	// float32 3.14 at addr 5,6 (big-endian register order)
	f32bits := math.Float32bits(3.14)
	holding[5] = uint16(f32bits >> 16)
	holding[6] = uint16(f32bits & 0xFFFF)
	// float64 e at addr 7..10
	f64bits := math.Float64bits(2.718281828459045)
	holding[7] = uint16(f64bits >> 48)
	holding[8] = uint16(f64bits >> 32)
	holding[9] = uint16(f64bits >> 16)
	holding[10] = uint16(f64bits & 0xFFFF)
	// uint64 at addr 11..14 (big-endian register order, high word first)
	u64val := uint64(0x0102030405060708)
	holding[11] = uint16(u64val >> 48)
	holding[12] = uint16(u64val >> 32)
	holding[13] = uint16(u64val >> 16)
	holding[14] = uint16(u64val & 0xFFFF)
	// int64 at addr 15..18
	i64val := int64(-9000000000)
	u64 := uint64(i64val)
	holding[15] = uint16(u64 >> 48)
	holding[16] = uint16(u64 >> 32)
	holding[17] = uint16(u64 >> 16)
	holding[18] = uint16(u64 & 0xFFFF)

	handler := &testModbusHandler{
		coils:          map[uint16]bool{0: true, 1: false},
		discreteInputs: map[uint16]bool{0: true, 1: false},
		holding:        holding,
		inputRegs: map[uint16]uint16{
			0: 999,
			1: 0x00CA, 2: 0xFE00, // uint32 0x00CAFE00
		},
	}
	d, cleanup := connectedClient(t, handler)
	defer cleanup()
	d.RLock()
	client := d.client
	d.RUnlock()

	cases := []struct {
		name string
		ai   addrInfo
		dt   core.DataType
		want any
	}{
		{"bool coil true", addrInfo{area: areaCoil, addr: 0}, core.TypeBool, true},
		{"bool coil false", addrInfo{area: areaCoil, addr: 1}, core.TypeBool, false},
		{"bool discrete true", addrInfo{area: areaDiscreteInput, addr: 0}, core.TypeBool, true},
		{"bool discrete false", addrInfo{area: areaDiscreteInput, addr: 1}, core.TypeBool, false},
		{"bool holding true", addrInfo{area: areaHoldingRegister, addr: 20}, core.TypeBool, true},
		{"bool holding false", addrInfo{area: areaHoldingRegister, addr: 21}, core.TypeBool, false},
		{"uint16 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeUint16, uint16(0x1234)},
		{"int16 holding", addrInfo{area: areaHoldingRegister, addr: 19}, core.TypeInt16, int16(-42)},
		{"uint32 holding", addrInfo{area: areaHoldingRegister, addr: 1}, core.TypeUint32, uint32(0xAABBCCDD)},
		{"int32 holding", addrInfo{area: areaHoldingRegister, addr: 3}, core.TypeInt32, int32(-12345)},
		{"float32 holding", addrInfo{area: areaHoldingRegister, addr: 5}, core.TypeFloat32, float32(3.14)},
		{"float64 holding", addrInfo{area: areaHoldingRegister, addr: 7}, core.TypeFloat64, 2.718281828459045},
		{"uint64 holding", addrInfo{area: areaHoldingRegister, addr: 11}, core.TypeUint64, uint64(0x0102030405060708)},
		{"int64 holding", addrInfo{area: areaHoldingRegister, addr: 15}, core.TypeInt64, int64(-9000000000)},
		{"uint16 input", addrInfo{area: areaInputRegister, addr: 0}, core.TypeUint16, uint16(999)},
		{"uint32 input", addrInfo{area: areaInputRegister, addr: 1}, core.TypeUint32, uint32(0x00CAFE00)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.readTag(client, tc.ai, tc.dt, tc.name)
			if err != nil {
				t.Fatalf("readTag(%s): unexpected error: %v", tc.name, err)
			}
			if !approxEqual(got, tc.want) {
				t.Errorf("readTag(%s): expected %v, got %v", tc.name, tc.want, got)
			}
		})
	}
}

// TestModbusReadTagErrorPaths covers the early-rejection branches of readTag:
// a non-bool type requested on a bit area (coil / discrete input) and an
// unsupported data type on a register area.
func TestModbusReadTagErrorPaths(t *testing.T) {
	handler := &testModbusHandler{
		coils:          map[uint16]bool{0: true},
		discreteInputs: map[uint16]bool{0: true},
		holding:        map[uint16]uint16{0: 1},
		inputRegs:      map[uint16]uint16{},
	}
	d, cleanup := connectedClient(t, handler)
	defer cleanup()
	d.RLock()
	client := d.client
	d.RUnlock()

	// Non-bool type on a coil area must fail.
	if _, err := d.readTag(client, addrInfo{area: areaCoil, addr: 0}, core.TypeUint16, "coil-u16"); err == nil {
		t.Error("expected error for uint16 on coil area, got nil")
	} else if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("expected 'not supported' error, got: %v", err)
	}

	// Non-bool type on a discrete-input area must fail.
	if _, err := d.readTag(client, addrInfo{area: areaDiscreteInput, addr: 0}, core.TypeUint16, "di-u16"); err == nil {
		t.Error("expected error for uint16 on discrete-input area, got nil")
	} else if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("expected 'not supported' error, got: %v", err)
	}

	// Unsupported data type on a register area must fail.
	if _, err := d.readTag(client, addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeString, "str"); err == nil {
		t.Error("expected error for unsupported data type, got nil")
	} else if !strings.Contains(err.Error(), "unsupported data type") {
		t.Errorf("expected 'unsupported data type' error, got: %v", err)
	}
}

// TestModbusReadTagClientErrors drives every readTag type branch through the
// client-error path by tearing down the server before reading. Each type's
// underlying Modbus call must surface a non-nil error instead of panicking.
func TestModbusReadTagClientErrors(t *testing.T) {
	handler := &testModbusHandler{
		coils:          map[uint16]bool{0: true},
		discreteInputs: map[uint16]bool{0: true},
		holding:        map[uint16]uint16{0: 1, 1: 1, 2: 1, 3: 1, 4: 1},
		inputRegs:      map[uint16]uint16{0: 1, 1: 1, 2: 1},
	}
	// Use a short timeout so a stalled read fails fast.
	server, port := startTestServer(t, handler)
	cfg := core.DriverConfig{
		Name: "readtag-err",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":     "127.0.0.1",
			"port":     port,
			"slave-id": 1,
			"timeout":  "300ms",
		},
		Tags: []core.TagConfig{{Name: "dummy", Address: "40001", Type: "uint16"}},
	}
	drv, err := NewModbusTCPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := drv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer drv.Stop()
	d := drv.(*ModbusTCPDriver)
	d.RLock()
	client := d.client
	d.RUnlock()
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	// Stop the server so all subsequent client reads fail.
	if err := server.Stop(); err != nil {
		t.Fatalf("server.Stop: %v", err)
	}

	combos := []struct {
		name string
		ai   addrInfo
		dt   core.DataType
	}{
		{"bool coil", addrInfo{area: areaCoil, addr: 0}, core.TypeBool},
		{"bool discrete", addrInfo{area: areaDiscreteInput, addr: 0}, core.TypeBool},
		{"bool holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeBool},
		{"uint16 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeUint16},
		{"int16 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeInt16},
		{"uint32 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeUint32},
		{"int32 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeInt32},
		{"float32 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeFloat32},
		{"float64 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeFloat64},
		{"uint64 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeUint64},
		{"int64 holding", addrInfo{area: areaHoldingRegister, addr: 0}, core.TypeInt64},
		{"uint16 input", addrInfo{area: areaInputRegister, addr: 0}, core.TypeUint16},
	}
	for _, tc := range combos {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.readTag(client, tc.ai, tc.dt, tc.name)
			if err == nil {
				t.Errorf("expected error reading %s after server stop, got nil", tc.name)
			}
		})
	}
}

// TestModbusHandleConnectionLostReconnects verifies that HandleConnectionLost
// transitions a connected driver to StateError, closes the connection, and
// starts the reconnect loop — which then succeeds because the server is still
// up, returning the driver to StateConnected.
func TestModbusHandleConnectionLostReconnects(t *testing.T) {
	handler := &testModbusHandler{
		coils:          map[uint16]bool{},
		discreteInputs: map[uint16]bool{},
		holding:        map[uint16]uint16{0: 42},
		inputRegs:      map[uint16]uint16{},
	}
	server, port := startTestServer(t, handler)
	defer server.Stop()

	cfg := core.DriverConfig{
		Name: "hcl-reconnect",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":                   "127.0.0.1",
			"port":                   port,
			"slave-id":               1,
			"timeout":                "1s",
			"reconnect-interval":     "50ms",
			"reconnect-max-interval": "100ms",
		},
		Tags: []core.TagConfig{{Name: "v", Address: "40001", Type: "uint16"}},
	}
	drv, err := NewModbusTCPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := drv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer drv.Stop()

	if drv.Status().State != core.StateConnected {
		t.Fatalf("expected connected before HandleConnectionLost, got %v", drv.Status().State)
	}

	d := drv.(*ModbusTCPDriver)
	d.HandleConnectionLost()

	// The reconnect loop must re-establish the connection because the server
	// is still running.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if drv.Status().State == core.StateConnected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if drv.Status().State != core.StateConnected {
		t.Errorf("expected StateConnected after reconnect, got %v", drv.Status().State)
	}
}

// TestModbusHandleConnectionLostNoOpWhenError verifies the guard in
// HandleConnectionLost: once the driver is already in StateError (with a
// reconnect loop running), a second call is a no-op.
func TestModbusHandleConnectionLostNoOpWhenError(t *testing.T) {
	handler := &testModbusHandler{
		coils:          map[uint16]bool{},
		discreteInputs: map[uint16]bool{},
		holding:        map[uint16]uint16{0: 42},
		inputRegs:      map[uint16]uint16{},
	}
	server, port := startTestServer(t, handler)

	cfg := core.DriverConfig{
		Name: "hcl-noop",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":                   "127.0.0.1",
			"port":                   port,
			"slave-id":               1,
			"timeout":                "300ms",
			"reconnect-interval":     "50ms",
			"reconnect-max-interval": "100ms",
		},
		Tags: []core.TagConfig{{Name: "v", Address: "40001", Type: "uint16"}},
	}
	drv, err := NewModbusTCPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusTCPDriver: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := drv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer drv.Stop()

	// Drop the server so the reconnect loop cannot succeed.
	if err := server.Stop(); err != nil {
		t.Fatalf("server.Stop: %v", err)
	}

	d := drv.(*ModbusTCPDriver)
	d.HandleConnectionLost()
	if drv.Status().State != core.StateError {
		t.Fatalf("expected StateError after HandleConnectionLost, got %v", drv.Status().State)
	}

	// The reconnect loop must be running and failing against the dead port.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if drv.Status().ReconnectCount > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if drv.Status().ReconnectCount == 0 {
		t.Error("expected ReconnectCount > 0 from the reconnect loop")
	}

	// A second call must be a no-op (guard returns early because state is Error).
	d.HandleConnectionLost()
	if drv.Status().State != core.StateError {
		t.Errorf("expected StateError to persist after no-op call, got %v", drv.Status().State)
	}
}

// TestModbusConnectFailureRTU verifies that the RTU connect path returns an
// error when the serial device does not exist.
func TestModbusConnectFailureRTU(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "rtu-fail",
		Type: "modbus-rtu",
		Settings: map[string]any{
			"serial-device": "/dev/ttyUSB99",
			"baud-rate":     9600,
			"timeout":       "300ms",
		},
		Tags: []core.TagConfig{{Name: "v", Address: "40001", Type: "uint16"}},
	}
	drv, err := NewModbusRTUDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusRTUDriver: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	d := drv.(*ModbusRTUDriver)
	if err := d.connect(); err == nil {
		t.Error("expected connect error for non-existent serial device, got nil")
	}
}

// TestModbusConnectFailureNet verifies that the RTU-over-TCP connect path
// returns an error when the target port is closed.
func TestModbusConnectFailureNet(t *testing.T) {
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
	}
	cfg := core.DriverConfig{
		Name: "net-fail",
		Type: "modbus-rtuovertcp",
		Settings: map[string]any{
			"host":    "127.0.0.1",
			"port":    port,
			"timeout": "300ms",
		},
		Tags: []core.TagConfig{{Name: "v", Address: "40001", Type: "uint16"}},
	}
	drv, err := NewModbusRTUOverTCPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusRTUOverTCPDriver: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	d := drv.(*ModbusNetDriver)
	if err := d.connect(); err == nil {
		t.Error("expected connect error for closed port, got nil")
	}
}

// TestModbusConnectFailureTLS verifies that the TLS connect path returns an
// error when the target server is unreachable.
func TestModbusConnectFailureTLS(t *testing.T) {
	certFile, keyFile := generateSelfSignedCert(t)
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
	}
	cfg := core.DriverConfig{
		Name: "tls-fail",
		Type: "modbus-tls",
		Settings: map[string]any{
			"host":      "127.0.0.1",
			"port":      port,
			"cert-file": certFile,
			"key-file":  keyFile,
			"ca-file":   certFile,
			"timeout":   "300ms",
		},
		Tags: []core.TagConfig{{Name: "v", Address: "40001", Type: "uint16"}},
	}
	drv, err := NewModbusTLSDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusTLSDriver: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	d := drv.(*ModbusTLSDriver)
	if err := d.connect(); err == nil {
		t.Error("expected connect error for unreachable TLS server, got nil")
	}
}
