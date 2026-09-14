package modbus

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

// countingHandler wraps testModbusHandler and counts read requests per
// area so tests can assert that batch reading actually collapsed multiple
// tags into a single Modbus request instead of N round-trips.
type countingHandler struct {
	inner         *testModbusHandler
	holdingReads  atomic.Int64
	inputReads    atomic.Int64
	coilReads     atomic.Int64
	discreteReads atomic.Int64
}

func (c *countingHandler) HandleCoils(req *mb.CoilsRequest) ([]bool, error) {
	if !req.IsWrite {
		c.coilReads.Add(1)
	}
	return c.inner.HandleCoils(req)
}

func (c *countingHandler) HandleDiscreteInputs(req *mb.DiscreteInputsRequest) ([]bool, error) {
	c.discreteReads.Add(1)
	return c.inner.HandleDiscreteInputs(req)
}

func (c *countingHandler) HandleHoldingRegisters(req *mb.HoldingRegistersRequest) ([]uint16, error) {
	if !req.IsWrite {
		c.holdingReads.Add(1)
	}
	return c.inner.HandleHoldingRegisters(req)
}

func (c *countingHandler) HandleInputRegisters(req *mb.InputRegistersRequest) ([]uint16, error) {
	c.inputReads.Add(1)
	return c.inner.HandleInputRegisters(req)
}

// startTestServer is a small helper shared by the batch tests: it stands
// up a loopback modbus server on a free port and returns it (the caller
// must defer server.Stop()).
func startTestServer(t *testing.T, handler mb.RequestHandler) (*mb.ModbusServer, int) {
	t.Helper()
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
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
	return server, port
}

func newTCPDriver(t *testing.T, port int, tags []core.TagConfig) core.Driver {
	t.Helper()
	cfg := core.DriverConfig{
		Name: "batch-plc",
		Type: "modbus-tcp",
		Settings: map[string]any{
			"host":     "127.0.0.1",
			"port":     port,
			"slave-id": 1,
			"timeout":  "2s",
		},
		Tags: tags,
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
	return drv
}

// TestModbusBatchReadContiguous verifies that N contiguous holding-register
// tags are fetched with a single Modbus request and that every value is
// decoded correctly.
func TestModbusBatchReadContiguous(t *testing.T) {
	const n = 10
	holding := make(map[uint16]uint16, n)
	for i := 0; i < n; i++ {
		holding[uint16(i)] = uint16(1000 + i)
	}
	inner := &testModbusHandler{
		coils:          map[uint16]bool{},
		discreteInputs: map[uint16]bool{},
		holding:        holding,
		inputRegs:      map[uint16]uint16{},
	}
	counter := &countingHandler{inner: inner}
	server, port := startTestServer(t, counter)
	defer server.Stop()

	tags := make([]core.TagConfig, n)
	names := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = fmt.Sprintf("r%d", i)
		tags[i] = core.TagConfig{Name: names[i], Address: fmt.Sprintf("%d", 40001+i), Type: "uint16"}
	}

	drv := newTCPDriver(t, port, tags)
	defer drv.Stop()

	values, err := drv.Read(context.Background(), names)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(values) != n {
		t.Fatalf("expected %d values, got %d", n, len(values))
	}
	for i := 0; i < n; i++ {
		if values[i].Quality != core.QualityGood {
			t.Errorf("r%d: expected good quality, got %v (err=%v)", i, values[i].Quality, values[i].Error)
		}
		if values[i].Value != uint16(1000+i) {
			t.Errorf("r%d: expected %d, got %v", i, 1000+i, values[i].Value)
		}
	}

	// The whole contiguous range must be a single batched request.
	if got := counter.holdingReads.Load(); got != 1 {
		t.Errorf("expected 1 batched holding-register request, got %d", got)
	}
}

// TestModbusBatchReadMixedTypes verifies that tags of different widths
// (uint16, uint32, float32, float64, int16) packed into contiguous
// registers are read in one request and decoded at the correct offsets.
func TestModbusBatchReadMixedTypes(t *testing.T) {
	// Layout (holding registers, zero-based addr):
	//   addr 0          : uint16 = 0x1234
	//   addr 1..2       : uint32 = 0xAABBCCDD  (high word first)
	//   addr 3..4       : float32 = 3.14
	//   addr 5..8       : float64 = 2.718281828459045
	//   addr 9          : int16  = -42
	holding := map[uint16]uint16{
		0: 0x1234,
		1: 0xAABB, 2: 0xCCDD,
	}
	f32bits := math.Float32bits(3.14)
	holding[3] = uint16(f32bits >> 16)
	holding[4] = uint16(f32bits & 0xFFFF)
	f64bits := math.Float64bits(2.718281828459045)
	holding[5] = uint16(f64bits >> 48)
	holding[6] = uint16(f64bits >> 32)
	holding[7] = uint16(f64bits >> 16)
	holding[8] = uint16(f64bits & 0xFFFF)
	neg42 := int16(-42)
	holding[9] = uint16(neg42) // 0xFFD6

	inner := &testModbusHandler{
		coils:          map[uint16]bool{},
		discreteInputs: map[uint16]bool{},
		holding:        holding,
		inputRegs:      map[uint16]uint16{},
	}
	counter := &countingHandler{inner: inner}
	server, port := startTestServer(t, counter)
	defer server.Stop()

	tags := []core.TagConfig{
		{Name: "u16", Address: "40001", Type: "uint16"},
		{Name: "u32", Address: "40002", Type: "uint32"},
		{Name: "f32", Address: "40004", Type: "float32"},
		{Name: "f64", Address: "40006", Type: "float64"},
		{Name: "i16", Address: "40010", Type: "int16"},
	}
	drv := newTCPDriver(t, port, tags)
	defer drv.Stop()

	values, err := drv.Read(context.Background(), []string{"u16", "u32", "f32", "f64", "i16"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(values) != 5 {
		t.Fatalf("expected 5 values, got %d", len(values))
	}

	check := func(name string, got any, want any) {
		t.Helper()
		switch w := want.(type) {
		case float32:
			g, ok := got.(float32)
			if !ok {
				t.Errorf("%s: expected float32, got %T(%v)", name, got, got)
				return
			}
			if math.Abs(float64(g-w)) > 1e-4 {
				t.Errorf("%s: expected %v, got %v", name, w, g)
			}
		case float64:
			g, ok := got.(float64)
			if !ok {
				t.Errorf("%s: expected float64, got %T(%v)", name, got, got)
				return
			}
			if math.Abs(g-w) > 1e-9 {
				t.Errorf("%s: expected %v, got %v", name, w, g)
			}
		default:
			if got != want {
				t.Errorf("%s: expected %v, got %v", name, want, got)
			}
		}
	}
	check("u16", values[0].Value, uint16(0x1234))
	check("u32", values[1].Value, uint32(0xAABBCCDD))
	check("f32", values[2].Value, float32(3.14))
	check("f64", values[3].Value, 2.718281828459045)
	check("i16", values[4].Value, int16(-42))

	// All five tags occupy one contiguous register span → one request.
	if got := counter.holdingReads.Load(); got != 1 {
		t.Errorf("expected 1 batched holding-register request, got %d", got)
	}
}

// TestModbusBatchReadCoils verifies that contiguous coils (bool) are
// fetched with a single FC01 request.
func TestModbusBatchReadCoils(t *testing.T) {
	const n = 8
	coils := make(map[uint16]bool, n)
	for i := 0; i < n; i++ {
		coils[uint16(i)] = i%2 == 0
	}
	inner := &testModbusHandler{
		coils:          coils,
		discreteInputs: map[uint16]bool{},
		holding:        map[uint16]uint16{},
		inputRegs:      map[uint16]uint16{},
	}
	counter := &countingHandler{inner: inner}
	server, port := startTestServer(t, counter)
	defer server.Stop()

	tags := make([]core.TagConfig, n)
	names := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = fmt.Sprintf("c%d", i)
		tags[i] = core.TagConfig{Name: names[i], Address: fmt.Sprintf("%05d", 1+i), Type: "bool"}
	}
	drv := newTCPDriver(t, port, tags)
	defer drv.Stop()

	values, err := drv.Read(context.Background(), names)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for i := 0; i < n; i++ {
		want := i%2 == 0
		if values[i].Value != want {
			t.Errorf("c%d: expected %v, got %v", i, want, values[i].Value)
		}
	}
	if got := counter.coilReads.Load(); got != 1 {
		t.Errorf("expected 1 batched coil request, got %d", got)
	}
}

// TestModbusBatchReadSparse verifies that tags with gaps between them are
// not merged across the gap (each contiguous run is its own batch) while
// values remain correct.
func TestModbusBatchReadSparse(t *testing.T) {
	holding := map[uint16]uint16{
		0: 10, 1: 11, 2: 12, // run A: addr 0..2
		// gap at addr 3..9
		10: 100, 11: 101, // run B: addr 10..11
	}
	inner := &testModbusHandler{
		coils:          map[uint16]bool{},
		discreteInputs: map[uint16]bool{},
		holding:        holding,
		inputRegs:      map[uint16]uint16{},
	}
	counter := &countingHandler{inner: inner}
	server, port := startTestServer(t, counter)
	defer server.Stop()

	tags := []core.TagConfig{
		{Name: "a0", Address: "40001", Type: "uint16"},
		{Name: "a1", Address: "40002", Type: "uint16"},
		{Name: "a2", Address: "40003", Type: "uint16"},
		{Name: "b0", Address: "40011", Type: "uint16"},
		{Name: "b1", Address: "40012", Type: "uint16"},
	}
	drv := newTCPDriver(t, port, tags)
	defer drv.Stop()

	values, err := drv.Read(context.Background(), []string{"a0", "a1", "a2", "b0", "b1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []uint16{10, 11, 12, 100, 101}
	for i, w := range want {
		if values[i].Value != w {
			t.Errorf("tag %d: expected %d, got %v", i, w, values[i].Value)
		}
	}
	// Two contiguous runs separated by a gap → two batched requests.
	if got := counter.holdingReads.Load(); got != 2 {
		t.Errorf("expected 2 batched holding-register requests, got %d", got)
	}
}

// TestModbusBatchReadMaxSizeSplit verifies that a run longer than
// MaxBatchSize (125) is split into multiple requests rather than exceeding
// the protocol limit.
func TestModbusBatchReadMaxSizeSplit(t *testing.T) {
	const n = 130
	holding := make(map[uint16]uint16, n)
	for i := 0; i < n; i++ {
		holding[uint16(i)] = uint16(5000 + i)
	}
	inner := &testModbusHandler{
		coils:          map[uint16]bool{},
		discreteInputs: map[uint16]bool{},
		holding:        holding,
		inputRegs:      map[uint16]uint16{},
	}
	counter := &countingHandler{inner: inner}
	server, port := startTestServer(t, counter)
	defer server.Stop()

	tags := make([]core.TagConfig, n)
	names := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = fmt.Sprintf("r%d", i)
		tags[i] = core.TagConfig{Name: names[i], Address: fmt.Sprintf("%d", 40001+i), Type: "uint16"}
	}
	drv := newTCPDriver(t, port, tags)
	defer drv.Stop()

	values, err := drv.Read(context.Background(), names)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for i := 0; i < n; i++ {
		if values[i].Value != uint16(5000+i) {
			t.Errorf("r%d: expected %d, got %v", i, 5000+i, values[i].Value)
		}
	}
	// 130 registers split at the 125-register limit → 2 requests.
	if got := counter.holdingReads.Load(); got != 2 {
		t.Errorf("expected 2 batched holding-register requests (125+5), got %d", got)
	}
}

// limitedBatchHandler rejects any holding-register read whose quantity is
// greater than one with a Modbus "illegal data address" exception, while
// serving single-register reads normally. This simulates a device that
// cannot service the batched range, forcing the driver to fall back to
// per-tag reads.
type limitedBatchHandler struct {
	inner *testModbusHandler
}

func (h *limitedBatchHandler) HandleCoils(req *mb.CoilsRequest) ([]bool, error) {
	return h.inner.HandleCoils(req)
}
func (h *limitedBatchHandler) HandleDiscreteInputs(req *mb.DiscreteInputsRequest) ([]bool, error) {
	return h.inner.HandleDiscreteInputs(req)
}
func (h *limitedBatchHandler) HandleHoldingRegisters(req *mb.HoldingRegistersRequest) ([]uint16, error) {
	if !req.IsWrite && req.Quantity > 1 {
		return nil, mb.ErrIllegalDataAddress
	}
	return h.inner.HandleHoldingRegisters(req)
}
func (h *limitedBatchHandler) HandleInputRegisters(req *mb.InputRegistersRequest) ([]uint16, error) {
	return h.inner.HandleInputRegisters(req)
}

// TestModbusBatchReadFallbackOnFailure verifies that when a batched read is
// rejected by the device, the driver falls back to individual per-tag reads
// so every tag still resolves correctly.
func TestModbusBatchReadFallbackOnFailure(t *testing.T) {
	holding := map[uint16]uint16{0: 7, 1: 8, 2: 9}
	inner := &testModbusHandler{
		coils:          map[uint16]bool{},
		discreteInputs: map[uint16]bool{},
		holding:        holding,
		inputRegs:      map[uint16]uint16{},
	}
	handler := &limitedBatchHandler{inner: inner}
	server, port := startTestServer(t, handler)
	defer server.Stop()

	tags := []core.TagConfig{
		{Name: "r0", Address: "40001", Type: "uint16"},
		{Name: "r1", Address: "40002", Type: "uint16"},
		{Name: "r2", Address: "40003", Type: "uint16"},
	}
	drv := newTCPDriver(t, port, tags)
	defer drv.Stop()

	values, err := drv.Read(context.Background(), []string{"r0", "r1", "r2"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []uint16{7, 8, 9}
	for i, w := range want {
		if values[i].Quality != core.QualityGood {
			t.Errorf("r%d: expected good quality, got %v (err=%v)", i, values[i].Quality, values[i].Error)
		}
		if values[i].Value != w {
			t.Errorf("r%d: expected %d, got %v", i, w, values[i].Value)
		}
	}
}

// TestModbusBatchReadMixedAreas verifies that tags spread across different
// Modbus areas (holding, input, coil, discrete) are batched per-area and
// all decoded correctly.
func TestModbusBatchReadMixedAreas(t *testing.T) {
	inner := &testModbusHandler{
		coils:          map[uint16]bool{0: true, 1: false, 2: true},
		discreteInputs: map[uint16]bool{0: false, 1: true},
		holding:        map[uint16]uint16{0: 111, 1: 222, 2: 333},
		inputRegs:      map[uint16]uint16{0: 444, 1: 555},
	}
	counter := &countingHandler{inner: inner}
	server, port := startTestServer(t, counter)
	defer server.Stop()

	tags := []core.TagConfig{
		{Name: "h0", Address: "40001", Type: "uint16"},
		{Name: "h1", Address: "40002", Type: "uint16"},
		{Name: "h2", Address: "40003", Type: "uint16"},
		{Name: "i0", Address: "30001", Type: "uint16"},
		{Name: "i1", Address: "30002", Type: "uint16"},
		{Name: "c0", Address: "00001", Type: "bool"},
		{Name: "c1", Address: "00002", Type: "bool"},
		{Name: "c2", Address: "00003", Type: "bool"},
		{Name: "d0", Address: "10001", Type: "bool"},
		{Name: "d1", Address: "10002", Type: "bool"},
	}
	drv := newTCPDriver(t, port, tags)
	defer drv.Stop()

	values, err := drv.Read(context.Background(), []string{"h0", "h1", "h2", "i0", "i1", "c0", "c1", "c2", "d0", "d1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(values) != 10 {
		t.Fatalf("expected 10 values, got %d", len(values))
	}
	checkU16 := func(idx int, want uint16) {
		t.Helper()
		if values[idx].Value != want {
			t.Errorf("tag %d: expected %d, got %v", idx, want, values[idx].Value)
		}
	}
	checkBool := func(idx int, want bool) {
		t.Helper()
		if values[idx].Value != want {
			t.Errorf("tag %d: expected %v, got %v", idx, want, values[idx].Value)
		}
	}
	checkU16(0, 111)
	checkU16(1, 222)
	checkU16(2, 333)
	checkU16(3, 444)
	checkU16(4, 555)
	checkBool(5, true)
	checkBool(6, false)
	checkBool(7, true)
	checkBool(8, false)
	checkBool(9, true)

	// One batched request per area.
	if got := counter.holdingReads.Load(); got != 1 {
		t.Errorf("expected 1 holding request, got %d", got)
	}
	if got := counter.inputReads.Load(); got != 1 {
		t.Errorf("expected 1 input request, got %d", got)
	}
	if got := counter.coilReads.Load(); got != 1 {
		t.Errorf("expected 1 coil request, got %d", got)
	}
	if got := counter.discreteReads.Load(); got != 1 {
		t.Errorf("expected 1 discrete request, got %d", got)
	}
}
