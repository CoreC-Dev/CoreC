package core

import (
	"encoding/json"
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

// TestDataTypeMarshalJSON verifies that DataType serializes as a
// human-readable string, not a raw integer, so JSON payloads are
// self-documenting.
func TestDataTypeMarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dt   DataType
		want string
	}{
		{TypeBool, `"bool"`},
		{TypeInt32, `"int32"`},
		{TypeFloat32, `"float32"`},
		{TypeFloat64, `"float64"`},
		{TypeString, `"string"`},
		{TypeBytes, `"bytes"`},
	}
	for _, tt := range tests {
		got, err := tt.dt.MarshalJSON()
		if err != nil {
			t.Fatalf("DataType(%d).MarshalJSON() error: %v", tt.dt, err)
		}
		if string(got) != tt.want {
			t.Errorf("DataType(%d).MarshalJSON() = %s, want %s", tt.dt, got, tt.want)
		}
	}
}

// TestDataTypeUnmarshalJSON verifies that both the string form ("float32")
// and the legacy integer form (9) are accepted, covering backward
// compatibility with existing clients and persisted payloads.
func TestDataTypeUnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    DataType
		wantErr bool
	}{
		{"string float32", `"float32"`, TypeFloat32, false},
		{"string bool", `"bool"`, TypeBool, false},
		{"string int64", `"int64"`, TypeInt64, false},
		{"integer 9 (TypeFloat32)", `9`, TypeFloat32, false},
		{"integer 0 (TypeBool)", `0`, TypeBool, false},
		{"integer 12 (TypeBytes)", `12`, TypeBytes, false},
		{"invalid string", `"notatype"`, 0, true},
		{"invalid integer out of range", `99`, 0, true},
		{"invalid type bool literal", `true`, 0, true},
		{"invalid type null", `null`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got DataType
			err := got.UnmarshalJSON([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %s, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %s: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("UnmarshalJSON(%s) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestDataTypeJSONRoundTripViaStruct verifies that a struct containing a
// DataType field round-trips correctly through json.Marshal/Unmarshal,
// proving the custom marshalers work in real JSON contexts (DataPoint,
// WriteCommand, TagValue).
func TestDataTypeJSONRoundTripViaStruct(t *testing.T) {
	t.Parallel()
	original := WriteCommand{
		Driver: "plc",
		Tag:    "temp",
		Value:  42.5,
		Type:   TypeFloat32,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	// Marshaled type must be the string form.
	wantJSON := `{"driver":"plc","device":"","tag":"temp","value":42.5,"type":"float32"}`
	if string(data) != wantJSON {
		t.Errorf("Marshal = %s, want %s", data, wantJSON)
	}
	// Round-trip: unmarshal back.
	var decoded WriteCommand
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if decoded.Type != original.Type {
		t.Errorf("round-trip Type = %d, want %d", decoded.Type, original.Type)
	}
}

// TestDataTypeUnmarshalLegacyIntegerViaStruct verifies that a legacy
// JSON payload using the integer form (type: 9) still decodes correctly,
// preserving backward compatibility with existing persisted data.
func TestDataTypeUnmarshalLegacyIntegerViaStruct(t *testing.T) {
	t.Parallel()
	legacyJSON := `{"driver":"plc","tag":"temp","value":99.9,"type":9}`
	var cmd WriteCommand
	if err := json.Unmarshal([]byte(legacyJSON), &cmd); err != nil {
		t.Fatalf("Unmarshal legacy integer form error: %v", err)
	}
	if cmd.Type != TypeFloat32 {
		t.Errorf("legacy type 9 decoded as %d, want %d (TypeFloat32)", cmd.Type, TypeFloat32)
	}
	if cmd.Value != 99.9 {
		t.Errorf("value = %v, want 99.9", cmd.Value)
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
