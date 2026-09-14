package s7

import (
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
