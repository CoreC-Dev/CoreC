package core

import (
	"testing"
)

// --- DataType tests ---

func TestParseDataType(t *testing.T) {
	t.Parallel()
	valid := map[string]DataType{
		"bool":    TypeBool,
		"int8":    TypeInt8,
		"int16":   TypeInt16,
		"int32":   TypeInt32,
		"int64":   TypeInt64,
		"uint8":   TypeUint8,
		"uint16":  TypeUint16,
		"uint32":  TypeUint32,
		"uint64":  TypeUint64,
		"float32": TypeFloat32,
		"float64": TypeFloat64,
		"string":  TypeString,
		"bytes":   TypeBytes,
	}
	for s, expected := range valid {
		dt, ok := ParseDataType(s)
		if !ok {
			t.Errorf("ParseDataType(%q) returned ok=false, want true", s)
		}
		if dt != expected {
			t.Errorf("ParseDataType(%q) = %d, want %d", s, dt, expected)
		}
	}

	// Invalid type
	_, ok := ParseDataType("invalid")
	if ok {
		t.Error("ParseDataType(\"invalid\") returned ok=true, want false")
	}
}

func TestDataTypeString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dt   DataType
		want string
	}{
		{TypeBool, "bool"},
		{TypeInt32, "int32"},
		{TypeFloat64, "float64"},
		{TypeString, "string"},
		{DataType(999), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.dt.String(); got != tt.want {
			t.Errorf("DataType(%d).String() = %q, want %q", tt.dt, got, tt.want)
		}
	}
}

// --- Quality tests ---

func TestQualityString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		q    Quality
		want string
	}{
		{QualityGood, "good"},
		{QualityBad, "bad"},
		{QualityUncertain, "uncertain"},
		{Quality(999), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.q.String(); got != tt.want {
			t.Errorf("Quality(%d).String() = %q, want %q", tt.q, got, tt.want)
		}
	}
}

// --- ConnState tests ---

func TestConnStateString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    ConnState
		want string
	}{
		{StateDisconnected, "disconnected"},
		{StateConnecting, "connecting"},
		{StateConnected, "connected"},
		{StateError, "error"},
		{ConnState(999), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("ConnState(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

// --- WriteResult tests (M24: Error is now a string) ---

func TestWriteResultJSON(t *testing.T) {
	t.Parallel()
	// WriteResult.Error should be a string, not an error interface.
	// This test verifies the field type is serializable.
	wr := WriteResult{Success: false, Error: "tag not found: foo"}
	if wr.Error != "tag not found: foo" {
		t.Errorf("WriteResult.Error = %q, want %q", wr.Error, "tag not found: foo")
	}

	// Success case with no error
	wrOK := WriteResult{Success: true}
	if wrOK.Error != "" {
		t.Errorf("WriteResult.Error = %q, want empty", wrOK.Error)
	}
}
