package s7

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/robinson/gos7"
)

func TestParseS7Address(t *testing.T) {
	tests := []struct {
		addrStr  string
		dt       core.DataType
		expected s7Address
	}{
		{
			addrStr: "DB1.DBD0",
			dt:      core.TypeFloat32,
			expected: s7Address{
				area:     areaDB,
				dbNumber: 1,
				start:    0,
				size:     4,
				isBit:    false,
			},
		},
		{
			addrStr: "DB10.DBW4",
			dt:      core.TypeInt16,
			expected: s7Address{
				area:     areaDB,
				dbNumber: 10,
				start:    4,
				size:     2,
				isBit:    false,
			},
		},
		{
			addrStr: "DB2.DBX0.3",
			dt:      core.TypeBool,
			expected: s7Address{
				area:     areaDB,
				dbNumber: 2,
				start:    0,
				bit:      3,
				size:     1,
				isBit:    true,
			},
		},
		{
			addrStr: "M0.1",
			dt:      core.TypeBool,
			expected: s7Address{
				area:  areaM,
				start: 0,
				bit:   1,
				size:  1,
				isBit: true,
			},
		},
		{
			addrStr: "MW10",
			dt:      core.TypeUint16,
			expected: s7Address{
				area:  areaM,
				start: 10,
				size:  2,
				isBit: false,
			},
		},
		{
			addrStr: "I0.5",
			dt:      core.TypeBool,
			expected: s7Address{
				area:  areaI,
				start: 0,
				bit:   5,
				size:  1,
				isBit: true,
			},
		},
		{
			addrStr: "QD4",
			dt:      core.TypeUint32,
			expected: s7Address{
				area:  areaQ,
				start: 4,
				size:  4,
				isBit: false,
			},
		},
	}

	for _, tt := range tests {
		addr, err := parseS7Address(tt.addrStr, tt.dt)
		if err != nil {
			t.Fatalf("parseS7Address(%s) error: %v", tt.addrStr, err)
		}
		if addr.area != tt.expected.area {
			t.Errorf("%s: expected area %v, got %v", tt.addrStr, tt.expected.area, addr.area)
		}
		if addr.dbNumber != tt.expected.dbNumber {
			t.Errorf("%s: expected dbNumber %d, got %d", tt.addrStr, tt.expected.dbNumber, addr.dbNumber)
		}
		if addr.start != tt.expected.start {
			t.Errorf("%s: expected start %d, got %d", tt.addrStr, tt.expected.start, addr.start)
		}
		if addr.bit != tt.expected.bit {
			t.Errorf("%s: expected bit %d, got %d", tt.addrStr, tt.expected.bit, addr.bit)
		}
		if addr.size != tt.expected.size {
			t.Errorf("%s: expected size %d, got %d", tt.addrStr, tt.expected.size, addr.size)
		}
		if addr.isBit != tt.expected.isBit {
			t.Errorf("%s: expected isBit %v, got %v", tt.addrStr, tt.expected.isBit, addr.isBit)
		}
	}
}

func TestS7EncodeDecode(t *testing.T) {
	h := &gos7.Helper{}

	// Test Float32 encoding & decoding
	addrFloat := s7Address{start: 0, size: 4}
	raw, err := encodeS7Value(float32(36.5), addrFloat, core.TypeFloat32, h)
	if err != nil {
		t.Fatalf("encodeS7Value error: %v", err)
	}
	if len(raw) != 4 {
		t.Fatalf("expected 4 bytes, got %d", len(raw))
	}

	val, err := decodeS7Buffer(raw, addrFloat, core.TypeFloat32, h)
	if err != nil {
		t.Fatalf("decodeS7Buffer error: %v", err)
	}
	if val.(float32) != float32(36.5) {
		t.Errorf("expected 36.5, got %v", val)
	}

	// Test Uint16 encoding & decoding
	addrUint := s7Address{start: 0, size: 2}
	rawUint, err := encodeS7Value(uint16(12345), addrUint, core.TypeUint16, h)
	if err != nil {
		t.Fatalf("encodeS7Value error: %v", err)
	}
	valUint, err := decodeS7Buffer(rawUint, addrUint, core.TypeUint16, h)
	if err != nil {
		t.Fatalf("decodeS7Buffer error: %v", err)
	}
	if valUint.(uint16) != uint16(12345) {
		t.Errorf("expected 12345, got %v", valUint)
	}
}

// TestS7DecodeBufferTooSmall verifies that decodeS7Buffer returns a graceful
// error instead of panicking when the buffer is too small for the configured
// data type (H14). This happens when an address kind and data type disagree,
// e.g. DB1.DBB0 (1-byte buffer) configured with type uint16.
func TestS7DecodeBufferTooSmall(t *testing.T) {
	h := &gos7.Helper{}

	tests := []struct {
		name string
		buf  []byte
		dt   core.DataType
		addr s7Address
	}{
		{"uint16 on 1-byte buffer", []byte{0x01}, core.TypeUint16, s7Address{start: 0, size: 1}},
		{"int16 on 1-byte buffer", []byte{0x01}, core.TypeInt16, s7Address{start: 0, size: 1}},
		{"uint32 on 2-byte buffer", []byte{0x01, 0x02}, core.TypeUint32, s7Address{start: 0, size: 2}},
		{"int32 on 2-byte buffer", []byte{0x01, 0x02}, core.TypeInt32, s7Address{start: 0, size: 2}},
		{"float32 on 1-byte buffer", []byte{0x01}, core.TypeFloat32, s7Address{start: 0, size: 1}},
		{"float64 on 4-byte buffer", []byte{0x01, 0x02, 0x03, 0x04}, core.TypeFloat64, s7Address{start: 0, size: 4}},
		{"uint8 on 0-byte buffer", []byte{}, core.TypeUint8, s7Address{start: 0, size: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This must return an error, not panic.
			val, err := decodeS7Buffer(tt.buf, tt.addr, tt.dt, h)
			if err == nil {
				t.Fatalf("expected error, got value %v", val)
			}
			if !strings.Contains(err.Error(), "buffer too small") {
				t.Errorf("expected 'buffer too small' error, got: %v", err)
			}
		})
	}
}

// TestS7DecodeBufferSizedOK verifies that correctly-sized buffers still decode
// successfully after the bounds-check change (a regression guard for H14).
func TestS7DecodeBufferSizedOK(t *testing.T) {
	h := &gos7.Helper{}

	// uint16 on a 2-byte buffer should still work.
	addr := s7Address{start: 0, size: 2}
	val, err := decodeS7Buffer([]byte{0x30, 0x39}, addr, core.TypeUint16, h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val.(uint16) != 12345 {
		t.Errorf("expected 12345, got %v", val)
	}
}

// ============================================================
// In-process Read/Write path tests using a mock gos7.Client.
//
// These tests exercise the driver's Read/Write logic — request building,
// area dispatch, encode/decode, error wrapping, batching, and transform
// application — without contacting a real PLC. A mockS7Client stands in
// for gos7.Client and records every AG read/write call.
// ============================================================

// initConnectedMockDriver builds a driver, runs Init with the supplied tags,
// then swaps in a mock client and marks the driver connected so Read/Write
// can be called in-process.
func initConnectedMockDriver(t *testing.T, tags []core.TagConfig, mock *mockS7Client) *S7Driver {
	t.Helper()
	d := newTestDriver(t, "mock-plc")
	cfg := core.DriverConfig{
		Name:     "mock-plc",
		Type:     "s7",
		Settings: map[string]any{"host": "127.0.0.1"},
		Tags:     tags,
	}
	if err := d.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	d.mu.Lock()
	d.client = mock
	d.state = core.StateConnected
	d.mu.Unlock()
	return d
}

// TestS7WriteEncode verifies that Write commands encode values into the
// correct S7 wire bytes for every memory area (DB, M, I, Q) and a range of
// data types. It checks the area, byte offset, size, and raw bytes that the
// driver hands to the underlying client — without a real PLC.
func TestS7WriteEncode(t *testing.T) {
	// Pre-compute IEEE-754 big-endian bytes for the float cases so the
	// expected values are self-documenting and not circular with the
	// driver's own encoder.
	f32Bytes := make([]byte, 4)
	binary.BigEndian.PutUint32(f32Bytes, math.Float32bits(36.5))
	f64Bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(f64Bytes, math.Float64bits(36.5))

	tests := []struct {
		name      string
		address   string
		typeStr   string
		value     any
		wantArea  string
		wantStart int
		wantSize  int
		wantData  []byte
	}{
		// Data Block area
		{"DB uint16", "DB1.DBW0", "uint16", uint16(12345), "DB", 0, 2, []byte{0x30, 0x39}},
		{"DB int16 negative", "DB1.DBW2", "int16", int16(-1), "DB", 2, 2, []byte{0xFF, 0xFF}},
		{"DB uint32", "DB1.DBD4", "uint32", uint32(0x075BCD15), "DB", 4, 4, []byte{0x07, 0x5B, 0xCD, 0x15}},
		{"DB int32 negative", "DB1.DBD8", "int32", int32(-1), "DB", 8, 4, []byte{0xFF, 0xFF, 0xFF, 0xFF}},
		{"DB float32", "DB1.DBD12", "float32", float32(36.5), "DB", 12, 4, f32Bytes},
		{"DB uint8", "DB1.DBB16", "uint8", 0xAB, "DB", 16, 1, []byte{0xAB}},
		// Merker area
		{"M uint16", "MW10", "uint16", uint16(300), "MB", 10, 2, []byte{0x01, 0x2C}},
		{"M int32", "MD20", "int32", int32(1000000), "MB", 20, 4, []byte{0x00, 0x0F, 0x42, 0x40}},
		{"M float64", "M30", "float64", float64(36.5), "MB", 30, 8, f64Bytes},
		// Input area (I)
		{"I uint16", "IW0", "uint16", uint16(1), "EB", 0, 2, []byte{0x00, 0x01}},
		// Output area (Q)
		{"Q uint32", "QD4", "uint32", uint32(42), "AB", 4, 4, []byte{0x00, 0x00, 0x00, 0x2A}},
		{"Q uint8 zero", "QB8", "uint8", 0, "AB", 8, 1, []byte{0x00}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockS7Client{}
			d := initConnectedMockDriver(t, []core.TagConfig{
				{Name: "tag", Address: tt.address, Type: tt.typeStr},
			}, mock)

			results, err := d.Write(context.Background(), []core.WriteCommand{
				{Tag: "tag", Value: tt.value, Type: mustDataType(tt.typeStr)},
			})
			if err != nil {
				t.Fatalf("Write returned error: %v", err)
			}
			if len(results) != 1 || !results[0].Success {
				t.Fatalf("expected 1 successful result, got %+v", results)
			}

			wc, ok := mock.lastWrite()
			if !ok {
				t.Fatal("expected a write call to be recorded")
			}
			if wc.area != tt.wantArea {
				t.Errorf("area = %q, want %q", wc.area, tt.wantArea)
			}
			if wc.start != tt.wantStart {
				t.Errorf("start = %d, want %d", wc.start, tt.wantStart)
			}
			if wc.size != tt.wantSize {
				t.Errorf("size = %d, want %d", wc.size, tt.wantSize)
			}
			if !bytesEqual(wc.data, tt.wantData) {
				t.Errorf("data = % X, want % X", wc.data, tt.wantData)
			}
		})
	}
}

// TestS7WriteEncodeBit verifies the bit-write read-modify-write path: the
// driver reads the containing byte, flips the target bit, and writes the
// byte back. Both the read and the write are recorded by the mock.
func TestS7WriteEncodeBit(t *testing.T) {
	tests := []struct {
		name       string
		address    string
		current    byte // existing byte value in the PLC
		value      bool
		wantWritten byte
	}{
		{"set bit 3 of zero byte", "M0.3", 0x00, true, 0x08},
		{"clear bit 1 of set byte", "M0.1", 0xFF, false, 0xFD},
		{"set bit 7 in DB", "DB1.DBX0.7", 0x00, true, 0x80},
		{"clear bit 0 in DB", "DB1.DBX0.0", 0x01, false, 0x00},
		{"set bit 5 of mixed byte", "Q0.5", 0x02, true, 0x22},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockS7Client{readData: []byte{tt.current}}
			d := initConnectedMockDriver(t, []core.TagConfig{
				{Name: "bit", Address: tt.address, Type: "bool"},
			}, mock)

			results, err := d.Write(context.Background(), []core.WriteCommand{
				{Tag: "bit", Value: tt.value, Type: core.TypeBool},
			})
			if err != nil {
				t.Fatalf("Write error: %v", err)
			}
			if !results[0].Success {
				t.Fatalf("write failed: %s", results[0].Error)
			}

			// A read must have happened first (read-modify-write).
			if _, ok := mock.lastRead(); !ok {
				t.Error("expected a read call before bit write")
			}
			wc, ok := mock.lastWrite()
			if !ok {
				t.Fatal("expected a write call to be recorded")
			}
			if wc.size != 1 {
				t.Errorf("write size = %d, want 1", wc.size)
			}
			if wc.data[0] != tt.wantWritten {
				t.Errorf("written byte = 0x%02X, want 0x%02X", wc.data[0], tt.wantWritten)
			}
		})
	}
}

// TestS7ReadDecode verifies that Read decodes raw S7 response bytes into the
// correct Go values for each supported data type, going through the full
// Read → readAddress → decodeS7Buffer path with a mock client.
func TestS7ReadDecode(t *testing.T) {
	f32Bytes := make([]byte, 4)
	binary.BigEndian.PutUint32(f32Bytes, math.Float32bits(36.5))
	f64Bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(f64Bytes, math.Float64bits(36.5))

	tests := []struct {
		name     string
		address  string
		typeStr  string
		readData []byte
		want     any
	}{
		{"bool true (bit 1)", "M0.1", "bool", []byte{0x02}, true},
		{"bool false (bit 3)", "M0.3", "bool", []byte{0x02}, false},
		{"uint16", "MW10", "uint16", []byte{0x30, 0x39}, uint16(12345)},
		{"int16 negative", "MW10", "int16", []byte{0xFF, 0xFF}, int16(-1)},
		{"uint32", "MD10", "uint32", []byte{0x00, 0x00, 0x00, 0x64}, uint32(100)},
		{"int32 negative", "MD10", "int32", []byte{0xFF, 0xFF, 0xFF, 0xFF}, int32(-1)},
		{"float32", "MD10", "float32", f32Bytes, float32(36.5)},
		{"float64", "M10", "float64", f64Bytes, float64(36.5)},
		{"uint8", "MB10", "uint8", []byte{0xAB}, uint8(0xAB)},
		{"int8 negative", "MB10", "int8", []byte{0x80}, int8(-128)},
		{"DB uint16", "DB1.DBW0", "uint16", []byte{0x01, 0x00}, uint16(256)},
		{"I bool", "I0.5", "bool", []byte{0x20}, true},
		{"Q uint32", "QD0", "uint32", []byte{0x00, 0x00, 0x00, 0x2A}, uint32(42)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockS7Client{readData: tt.readData}
			d := initConnectedMockDriver(t, []core.TagConfig{
				{Name: "tag", Address: tt.address, Type: tt.typeStr},
			}, mock)

			vals, err := d.Read(context.Background(), []string{"tag"})
			if err != nil {
				t.Fatalf("Read error: %v", err)
			}
			if len(vals) != 1 {
				t.Fatalf("expected 1 value, got %d", len(vals))
			}
			if vals[0].Quality != core.QualityGood {
				t.Fatalf("quality = %v, want good: %v", vals[0].Quality, vals[0].Error)
			}
			if !valuesEqual(vals[0].Value, tt.want) {
				t.Errorf("value = %v (%T), want %v (%T)", vals[0].Value, vals[0].Value, tt.want, tt.want)
			}
		})
	}
}

// TestS7ReadDecodeString verifies S7 string decoding. Strings use a 2-byte
// header (max-len, actual-len) followed by the characters; the address-size
// inference for strings is limited, so this tests decodeS7Buffer directly
// with a properly laid-out buffer.
func TestS7ReadDecodeString(t *testing.T) {
	h := &gos7.Helper{}
	// S7 string "AB": [maxLen=2, actualLen=2, 'A', 'B']
	buf := []byte{0x02, 0x02, 0x41, 0x42}
	addr := s7Address{start: 0, size: 4}
	val, err := decodeS7Buffer(buf, addr, core.TypeString, h)
	if err != nil {
		t.Fatalf("decodeS7Buffer error: %v", err)
	}
	if val.(string) != "AB" {
		t.Errorf("expected \"AB\", got %q", val)
	}

	// Empty string.
	bufEmpty := []byte{0x00, 0x00}
	val, err = decodeS7Buffer(bufEmpty, s7Address{size: 2}, core.TypeString, h)
	if err != nil {
		t.Fatalf("decodeS7Buffer error: %v", err)
	}
	if val.(string) != "" {
		t.Errorf("expected empty string, got %q", val)
	}
}

// TestS7ReadError verifies that an error from the underlying PLC client
// during Read is wrapped, reported as QualityBad, and counted — without
// propagating a top-level error (the driver returns per-tag results).
func TestS7ReadError(t *testing.T) {
	mock := &mockS7Client{readErr: errMockPlc}
	d := initConnectedMockDriver(t, []core.TagConfig{
		{Name: "temp", Address: "DB1.DBD0", Type: "float32"},
	}, mock)

	beforeErr := d.errorCount.Load()
	vals, err := d.Read(context.Background(), []string{"temp"})
	if err != nil {
		t.Fatalf("Read should not return a top-level error for a per-tag failure: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 value, got %d", len(vals))
	}
	if vals[0].Quality != core.QualityBad {
		t.Errorf("quality = %v, want bad", vals[0].Quality)
	}
	if vals[0].Error == nil {
		t.Fatal("expected per-tag error to be set")
	}
	if !strings.Contains(vals[0].Error.Error(), "s7 read error") {
		t.Errorf("error = %q, want substring 's7 read error'", vals[0].Error.Error())
	}
	if !strings.Contains(vals[0].Error.Error(), "mock plc") {
		t.Errorf("error = %q, want substring 'mock plc' (wrapped)", vals[0].Error.Error())
	}
	if d.errorCount.Load() != beforeErr+1 {
		t.Errorf("errorCount = %d, want %d", d.errorCount.Load(), beforeErr+1)
	}

	// The lastError field must record the failure.
	if !strings.Contains(d.Status().LastError, "s7 read error") {
		t.Errorf("Status().LastError = %q, want substring 's7 read error'", d.Status().LastError)
	}
}

// TestS7WriteError verifies that an error from the underlying PLC client
// during Write is captured per-command and counted, while other commands in
// the same batch still succeed.
func TestS7WriteError(t *testing.T) {
	mock := &mockS7Client{writeErr: errMockPlc}
	d := initConnectedMockDriver(t, []core.TagConfig{
		{Name: "a", Address: "MW0", Type: "uint16"},
	}, mock)

	beforeErr := d.errorCount.Load()
	results, err := d.Write(context.Background(), []core.WriteCommand{
		{Tag: "a", Value: uint16(1), Type: core.TypeUint16},
	})
	if err != nil {
		t.Fatalf("Write should not return a top-level error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Success {
		t.Error("expected failure, got success")
	}
	if !strings.Contains(results[0].Error, "mock plc") {
		t.Errorf("error = %q, want substring 'mock plc'", results[0].Error)
	}
	if d.errorCount.Load() != beforeErr+1 {
		t.Errorf("errorCount = %d, want %d", d.errorCount.Load(), beforeErr+1)
	}
}

// TestS7WriteTagNotFound verifies that writing an unknown tag produces a
// failed WriteResult without calling the client.
func TestS7WriteTagNotFound(t *testing.T) {
	mock := &mockS7Client{}
	d := initConnectedMockDriver(t, []core.TagConfig{
		{Name: "known", Address: "MW0", Type: "uint16"},
	}, mock)

	results, err := d.Write(context.Background(), []core.WriteCommand{
		{Tag: "unknown", Value: uint16(1), Type: core.TypeUint16},
	})
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if results[0].Success {
		t.Error("expected failure for unknown tag")
	}
	if !strings.Contains(results[0].Error, "tag not found") {
		t.Errorf("error = %q, want substring 'tag not found'", results[0].Error)
	}
	if _, ok := mock.lastWrite(); ok {
		t.Error("client should not be called for an unknown tag")
	}
}

// TestS7ReadTagNotFound verifies that reading an unknown tag yields a
// QualityBad result with a "tag not found" error.
func TestS7ReadTagNotFound(t *testing.T) {
	mock := &mockS7Client{}
	d := initConnectedMockDriver(t, []core.TagConfig{
		{Name: "known", Address: "MW0", Type: "uint16"},
	}, mock)

	vals, err := d.Read(context.Background(), []string{"unknown"})
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 value, got %d", len(vals))
	}
	if vals[0].Quality != core.QualityBad {
		t.Errorf("quality = %v, want bad", vals[0].Quality)
	}
	if vals[0].Error == nil || !strings.Contains(vals[0].Error.Error(), "tag not found") {
		t.Errorf("error = %v, want substring 'tag not found'", vals[0].Error)
	}
}

// TestS7ReadTransform verifies that scale and offset are applied to read
// values via util.ApplyTransform.
func TestS7ReadTransform(t *testing.T) {
	mock := &mockS7Client{readData: []byte{0x00, 0x64}} // uint16 100
	d := initConnectedMockDriver(t, []core.TagConfig{
		{Name: "scaled", Address: "MW0", Type: "uint16", Scale: 2.0, Offset: 1.0},
	}, mock)

	vals, err := d.Read(context.Background(), []string{"scaled"})
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	// ApplyTransform: 100 * 2.0 + 1.0 = 201.0 (float64 for uint16 input)
	want := float64(201.0)
	got, ok := vals[0].Value.(float64)
	if !ok {
		t.Fatalf("value type = %T, want float64", vals[0].Value)
	}
	if got != want {
		t.Errorf("scaled value = %v, want %v", got, want)
	}
}

// TestS7ReadBatchCounters verifies that a successful read updates the
// readCount counter and lastRead timestamp.
func TestS7ReadBatchCounters(t *testing.T) {
	mock := &mockS7Client{readData: []byte{0x01}}
	d := initConnectedMockDriver(t, []core.TagConfig{
		{Name: "a", Address: "M0.0", Type: "bool"},
		{Name: "b", Address: "M0.1", Type: "bool"},
	}, mock)

	beforeRead := d.readCount.Load()
	vals, err := d.Read(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(vals) != 2 {
		t.Fatalf("expected 2 values, got %d", len(vals))
	}
	if d.readCount.Load() != beforeRead+2 {
		t.Errorf("readCount = %d, want %d", d.readCount.Load(), beforeRead+2)
	}
	if d.Status().LastRead.IsZero() {
		t.Error("LastRead should be set after a read")
	}
}

// TestS7InitInvalidConfig is a comprehensive table-driven test of every
// Init error path: missing/invalid host and malformed tag addresses.
func TestS7InitInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  core.DriverConfig
		wantErr string
	}{
		{
			name:    "missing host key",
			config:  core.DriverConfig{Name: "t", Settings: map[string]any{}},
			wantErr: "host is required",
		},
		{
			name:    "host is integer",
			config:  core.DriverConfig{Name: "t", Settings: map[string]any{"host": 19216801}},
			wantErr: "host is required",
		},
		{
			name:    "host is nil",
			config:  core.DriverConfig{Name: "t", Settings: map[string]any{"host": nil}},
			wantErr: "host is required",
		},
		{
			name:    "nil settings map",
			config:  core.DriverConfig{Name: "t", Settings: nil},
			wantErr: "host is required",
		},
		{
			name: "unrecognized tag address",
			config: core.DriverConfig{
				Name:     "t",
				Settings: map[string]any{"host": "10.0.0.1"},
				Tags:     []core.TagConfig{{Name: "bad", Address: "ZZ123", Type: "int16"}},
			},
			wantErr: "invalid s7 address",
		},
		{
			name: "empty tag address",
			config: core.DriverConfig{
				Name:     "t",
				Settings: map[string]any{"host": "10.0.0.1"},
				Tags:     []core.TagConfig{{Name: "bad", Address: "", Type: "int16"}},
			},
			wantErr: "invalid s7 address",
		},
		{
			name: "second tag invalid among valid ones",
			config: core.DriverConfig{
				Name:     "t",
				Settings: map[string]any{"host": "10.0.0.1"},
				Tags: []core.TagConfig{
					{Name: "ok", Address: "MW0", Type: "uint16"},
					{Name: "bad", Address: "XYZ", Type: "int16"},
				},
			},
			wantErr: "tag bad: invalid s7 address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDriver(t, "t")
			err := d.Init(context.Background(), tt.config)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestS7InitRackSlotCoercion documents that rack and slot are coerced from
// the config (accepting int and float64/YAML-style numbers) and fall back to
// defaults when absent or non-numeric. The driver does not validate rack/slot
// ranges at Init time — they are only checked by the PLC on connect.
func TestS7InitRackSlotCoercion(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		wantRack int
		wantSlot int
	}{
		{"defaults", map[string]any{"host": "h"}, 0, 2},
		{"explicit ints", map[string]any{"host": "h", "rack": 1, "slot": 1}, 1, 1},
		{"yaml float64", map[string]any{"host": "h", "rack": float64(2), "slot": float64(0)}, 2, 0},
		{"non-numeric falls back", map[string]any{"host": "h", "rack": "a", "slot": "b"}, 0, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDriver(t, "t")
			if err := d.Init(context.Background(), core.DriverConfig{
				Name: "t", Type: "s7", Settings: tt.settings,
			}); err != nil {
				t.Fatalf("Init error: %v", err)
			}
			if d.rack != tt.wantRack {
				t.Errorf("rack = %d, want %d", d.rack, tt.wantRack)
			}
			if d.slot != tt.wantSlot {
				t.Errorf("slot = %d, want %d", d.slot, tt.wantSlot)
			}
		})
	}
}

// TestS7AddressParsing is a comprehensive table-driven test of the S7
// address parser, covering all four memory areas, the size-kind suffixes
// (X/B/W/D), bit offsets, the German E/A aliases for I/Q, case insensitivity,
// and error cases.
func TestS7AddressParsing(t *testing.T) {
	tests := []struct {
		name      string
		addrStr   string
		dt        core.DataType
		wantArea  s7Area
		wantDB    int
		wantStart int
		wantBit   uint
		wantSize  int
		wantBit_  bool
		wantErr   bool
	}{
		// Data Block
		{"DB bit", "DB1.DBX0.0", core.TypeBool, areaDB, 1, 0, 0, 1, true, false},
		{"DB byte", "DB1.DBB0", core.TypeUint8, areaDB, 1, 0, 0, 1, false, false},
		{"DB word", "DB10.DBW4", core.TypeInt16, areaDB, 10, 4, 0, 2, false, false},
		{"DB dword", "DB100.DBD4", core.TypeFloat32, areaDB, 100, 4, 0, 4, false, false},
		{"DB bit high offset", "DB255.DBX100.7", core.TypeBool, areaDB, 255, 100, 7, 1, true, false},
		{"DB lowercase", "db1.dbx0.3", core.TypeBool, areaDB, 1, 0, 3, 1, true, false},

		// Merker (M)
		{"M bit", "M10.0", core.TypeBool, areaM, 0, 10, 0, 1, true, false},
		{"M byte", "MB10", core.TypeUint8, areaM, 0, 10, 0, 1, false, false},
		{"M word", "MW20", core.TypeUint16, areaM, 0, 20, 0, 2, false, false},
		{"M dword", "MD30", core.TypeUint32, areaM, 0, 30, 0, 4, false, false},
		{"M bit 7", "M0.7", core.TypeBool, areaM, 0, 0, 7, 1, true, false},
		{"M bare bool inferred", "M5", core.TypeBool, areaM, 0, 5, 0, 1, true, false},

		// Inputs (I and German E)
		{"I bit", "I0.0", core.TypeBool, areaI, 0, 0, 0, 1, true, false},
		{"I byte", "IB1", core.TypeUint8, areaI, 0, 1, 0, 1, false, false},
		{"I word", "IW2", core.TypeUint16, areaI, 0, 2, 0, 2, false, false},
		{"I dword", "ID4", core.TypeUint32, areaI, 0, 4, 0, 4, false, false},
		{"E bit (German alias)", "E0.5", core.TypeBool, areaI, 0, 0, 5, 1, true, false},
		{"E word (German alias)", "EW2", core.TypeUint16, areaI, 0, 2, 0, 2, false, false},

		// Outputs (Q and German A)
		{"Q bit", "Q0.0", core.TypeBool, areaQ, 0, 0, 0, 1, true, false},
		{"Q byte", "QB1", core.TypeUint8, areaQ, 0, 1, 0, 1, false, false},
		{"Q word", "QW2", core.TypeUint16, areaQ, 0, 2, 0, 2, false, false},
		{"Q dword", "QD4", core.TypeUint32, areaQ, 0, 4, 0, 4, false, false},
		{"A bit (German alias)", "A0.3", core.TypeBool, areaQ, 0, 0, 3, 1, true, false},
		{"A dword (German alias)", "AD4", core.TypeUint32, areaQ, 0, 4, 0, 4, false, false},

		// Whitespace trimming
		{"leading/trailing whitespace", "  MW10  ", core.TypeUint16, areaM, 0, 10, 0, 2, false, false},

		// Errors
		{"empty string", "", core.TypeInt16, 0, 0, 0, 0, 0, false, true},
		{"garbage", "ZZ123", core.TypeInt16, 0, 0, 0, 0, 0, false, true},
		{"DB missing kind", "DB1.0", core.TypeInt16, 0, 0, 0, 0, 0, false, true},
		{"lone prefix", "M", core.TypeInt16, 0, 0, 0, 0, 0, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := parseS7Address(tt.addrStr, tt.dt)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %+v", tt.addrStr, addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseS7Address(%q) error: %v", tt.addrStr, err)
			}
			if addr.area != tt.wantArea {
				t.Errorf("area = %v, want %v", addr.area, tt.wantArea)
			}
			if addr.dbNumber != tt.wantDB {
				t.Errorf("dbNumber = %d, want %d", addr.dbNumber, tt.wantDB)
			}
			if addr.start != tt.wantStart {
				t.Errorf("start = %d, want %d", addr.start, tt.wantStart)
			}
			if addr.bit != tt.wantBit {
				t.Errorf("bit = %d, want %d", addr.bit, tt.wantBit)
			}
			if addr.size != tt.wantSize {
				t.Errorf("size = %d, want %d", addr.size, tt.wantSize)
			}
			if addr.isBit != tt.wantBit_ {
				t.Errorf("isBit = %v, want %v", addr.isBit, tt.wantBit_)
			}
		})
	}
}

// TestS7WriteEncodeUnsupportedType verifies that writing an unsupported type
// (string) returns a per-command failure with a clear error.
func TestS7WriteEncodeUnsupportedType(t *testing.T) {
	mock := &mockS7Client{}
	d := initConnectedMockDriver(t, []core.TagConfig{
		{Name: "s", Address: "MB0", Type: "string"},
	}, mock)

	results, err := d.Write(context.Background(), []core.WriteCommand{
		{Tag: "s", Value: "hello", Type: core.TypeString},
	})
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if results[0].Success {
		t.Fatal("expected failure for string write")
	}
	if !strings.Contains(results[0].Error, "not supported") {
		t.Errorf("error = %q, want substring 'not supported'", results[0].Error)
	}
}

// ----- small test helpers -----

func mustDataType(s string) core.DataType {
	dt, ok := core.ParseDataType(s)
	if !ok {
		panic("unknown data type: " + s)
	}
	return dt
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func valuesEqual(a, b any) bool {
	switch av := a.(type) {
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case uint8:
		bv, ok := b.(uint8)
		return ok && av == bv
	case int8:
		bv, ok := b.(int8)
		return ok && av == bv
	case uint16:
		bv, ok := b.(uint16)
		return ok && av == bv
	case int16:
		bv, ok := b.(int16)
		return ok && av == bv
	case uint32:
		bv, ok := b.(uint32)
		return ok && av == bv
	case int32:
		bv, ok := b.(int32)
		return ok && av == bv
	case float32:
		bv, ok := b.(float32)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	default:
		return a == b
	}
}
