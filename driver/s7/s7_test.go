package s7

import (
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
