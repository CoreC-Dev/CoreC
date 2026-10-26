package core

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
)

// roundTripDataPoint marshals dp and decodes it back, the exact path
// every JSON boundary (offline-buffer replay, chained-core inbound
// parsing, tag streams) takes through DataPoint.
func roundTripDataPoint(t *testing.T, dp DataPoint) DataPoint {
	t.Helper()
	payload, err := json.Marshal(dp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out DataPoint
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// TestDataPointValuePrecisionRoundTrip asserts that typed numeric values
// survive a JSON round-trip bit-exactly and with their declared Go type —
// the default `any` decode coerces every number to float64, silently
// rounding integers above ±2^53 (9007199254740993 → 9007199254740992).
func TestDataPointValuePrecisionRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		dp   DataPoint
		want any
	}{
		{"int64 above 2^53", DataPoint{Tag: "t", Value: int64(9007199254740993), Type: TypeInt64}, int64(9007199254740993)},
		{"int64 minimum", DataPoint{Tag: "t", Value: int64(math.MinInt64), Type: TypeInt64}, int64(math.MinInt64)},
		{"uint64 maximum", DataPoint{Tag: "t", Value: uint64(math.MaxUint64), Type: TypeUint64}, uint64(math.MaxUint64)},
		{"int32 negative", DataPoint{Tag: "t", Value: int32(math.MinInt32), Type: TypeInt32}, int32(math.MinInt32)},
		{"uint16", DataPoint{Tag: "t", Value: uint16(65535), Type: TypeUint16}, uint16(65535)},
		{"int8", DataPoint{Tag: "t", Value: int8(-128), Type: TypeInt8}, int8(-128)},
		{"uint8", DataPoint{Tag: "t", Value: uint8(255), Type: TypeUint8}, uint8(255)},
		{"float32", DataPoint{Tag: "t", Value: float32(23.5), Type: TypeFloat32}, float32(23.5)},
		{"float64", DataPoint{Tag: "t", Value: math.MaxFloat64, Type: TypeFloat64}, math.MaxFloat64},
		{"bool", DataPoint{Tag: "t", Value: true, Type: TypeBool}, true},
		{"string", DataPoint{Tag: "t", Value: "s3", Type: TypeString}, "s3"},
		{"bytes", DataPoint{Tag: "t", Value: []byte{0xde, 0xad, 0xbe, 0xef}, Type: TypeBytes}, []byte{0xde, 0xad, 0xbe, 0xef}},
		{"nil value", DataPoint{Tag: "t", Value: nil, Type: TypeFloat64}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := roundTripDataPoint(t, tc.dp)
			// Slices are not comparable with == (bytes case); compare
			// via bytes.Equal there, identity elsewhere.
			if b, ok := tc.want.([]byte); ok {
				gb, ok := got.Value.([]byte)
				if !ok || !bytes.Equal(gb, b) {
					t.Fatalf("round-trip corrupted %v: got %v (%T)", tc.want, got.Value, got.Value)
				}
				return
			}
			if got.Value != tc.want {
				t.Fatalf("round-trip corrupted %v (%T): got %v (%T)", tc.want, tc.want, got.Value, got.Value)
			}
		})
	}
}

// TestDataPointUntypedAndMismatchedFallbacks pins the fallback rule:
// when the value token does not fit the declared type's JSON form — or
// the type is absent entirely — the default `any` decode applies, which
// is exactly the pre-fix behavior for those payloads.
func TestDataPointUntypedAndMismatchedFallbacks(t *testing.T) {
	t.Run("absent type keeps default number decode", func(t *testing.T) {
		var dp DataPoint
		if err := json.Unmarshal([]byte(`{"driver":"d","tag":"t","value":42}`), &dp); err != nil {
			t.Fatal(err)
		}
		if v, ok := dp.Value.(float64); !ok || v != 42 {
			t.Fatalf("expected default float64(42), got %T (%v)", dp.Value, dp.Value)
		}
	})
	t.Run("string token with int64 type falls back to string", func(t *testing.T) {
		var dp DataPoint
		if err := json.Unmarshal([]byte(`{"tag":"t","value":"abc","type":"int64"}`), &dp); err != nil {
			t.Fatal(err)
		}
		if dp.Value != "abc" {
			t.Fatalf("expected string fallback, got %T (%v)", dp.Value, dp.Value)
		}
	})
	t.Run("exponent form int falls back to float64", func(t *testing.T) {
		var dp DataPoint
		if err := json.Unmarshal([]byte(`{"tag":"t","value":1e16,"type":"int64"}`), &dp); err != nil {
			t.Fatal(err)
		}
		if v, ok := dp.Value.(float64); !ok || v != 1e16 {
			t.Fatalf("expected float64 fallback for exponent form, got %T (%v)", dp.Value, dp.Value)
		}
	})
	t.Run("null stays nil", func(t *testing.T) {
		var dp DataPoint
		if err := json.Unmarshal([]byte(`{"tag":"t","value":null,"type":"float64"}`), &dp); err != nil {
			t.Fatal(err)
		}
		if dp.Value != nil {
			t.Fatalf("expected nil, got %T (%v)", dp.Value, dp.Value)
		}
	})
}

// TestWriteCommandValuePrecisionRoundTrip: a cloud-issued int64 write
// above ±2^53 must reach the driver bit-exactly through MQTT command
// decoding and the API — previously the float64 coercion made the PLC
// receive a rounded register payload.
func TestWriteCommandValuePrecisionRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want any
	}{
		{"int64 above 2^53", `{"driver":"d","tag":"t","value":9007199254740993,"type":"int64"}`, int64(9007199254740993)},
		{"uint64 maximum", `{"driver":"d","tag":"t","value":18446744073709551615,"type":"uint64"}`, uint64(math.MaxUint64)},
		{"legacy integer type form", `{"driver":"d","tag":"t","value":9007199254740993,"type":4}`, int64(9007199254740993)}, // 4 = int64 iota
		{"float32 declared", `{"driver":"d","tag":"t","value":23.5,"type":"float32"}`, float32(23.5)},
		{"absent type keeps float64", `{"driver":"d","tag":"t","value":9007199254740993}`, float64(9007199254740992)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cmd WriteCommand
			if err := json.Unmarshal([]byte(tc.raw), &cmd); err != nil {
				t.Fatal(err)
			}
			if cmd.Value != tc.want {
				t.Fatalf("write command corrupted: want %v (%T), got %v (%T)", tc.want, tc.want, cmd.Value, cmd.Value)
			}
		})
	}
}

// TestDecodeValueByTypeBranches covers the token-decoder helpers'
// fallback branches directly: these paths guard malformed or untyped
// payloads whose outer JSON is valid, so the public round-trip cannot
// always reach them. All assertions pin the pre-fix behavior: the
// default `any` decode (numbers → float64), never a silent error.
func TestDecodeValueByTypeBranches(t *testing.T) {
	t.Parallel()

	t.Run("int16 and uint32 narrow exactly", func(t *testing.T) {
		if got := decodeValueByType([]byte("-32768"), TypeInt16); got != int16(-32768) {
			t.Errorf("int16 narrow: got %v (%T)", got, got)
		}
		if got := decodeValueByType([]byte("4294967295"), TypeUint32); got != uint32(4294967295) {
			t.Errorf("uint32 narrow: got %v (%T)", got, got)
		}
	})

	t.Run("unsigned non-number and unparseable tokens fall back", func(t *testing.T) {
		if got := decodeValueByType([]byte(`"abc"`), TypeUint64); got != "abc" {
			t.Errorf("uint64 string token: got %v (%T)", got, got)
		}
		if got := decodeValueByType([]byte("1e16"), TypeUint64); got != float64(1e16) {
			t.Errorf("uint64 exponent token: got %v (%T)", got, got)
		}
	})

	t.Run("float non-number token falls back", func(t *testing.T) {
		if got := decodeValueByType([]byte(`"x"`), TypeFloat32); got != "x" {
			t.Errorf("float32 string token: got %v (%T)", got, got)
		}
		// Overflowing float literals fail ParseFloat (ErrRange) and the
		// default decode equally — the defensive fallback yields nil
		// rather than a wrong value.
		if got := decodeValueByType([]byte("1e999"), TypeFloat64); got != nil {
			t.Errorf("float64 overflow token: got %v (%T), want nil", got, got)
		}
	})

	t.Run("string and bytes types reject mismatched tokens", func(t *testing.T) {
		if got := decodeValueByType([]byte("123"), TypeString); got != float64(123) {
			t.Errorf("string type with number token: got %v (%T)", got, got)
		}
		if got := decodeValueByType([]byte("123"), TypeBytes); got != float64(123) {
			t.Errorf("bytes type with number token: got %v (%T)", got, got)
		}
		// Valid string token, but not base64: falls back to the string.
		if got := decodeValueByType([]byte(`"!!!"`), TypeBytes); got != "!!!" {
			t.Errorf("bytes type with non-base64 string: got %v (%T)", got, got)
		}
	})

	t.Run("unknown DataType falls back to the default decode", func(t *testing.T) {
		if got := decodeValueByType([]byte("42"), DataType(999)); got != float64(42) {
			t.Errorf("unknown type: got %v (%T)", got, got)
		}
	})

	t.Run("decodeDefaultAny returns nil on invalid JSON", func(t *testing.T) {
		if got := decodeDefaultAny([]byte("not json")); got != nil {
			t.Errorf("invalid JSON: got %v (%T)", got, got)
		}
	})

	t.Run("probeRawValue returns nil on invalid JSON", func(t *testing.T) {
		if got := probeRawValue([]byte(`{invalid`)); got != nil {
			t.Errorf("invalid JSON: got %v", got)
		}
	})
}

// TestUnmarshalJSONFieldErrors pins the error paths: a payload whose
// fields do not match the struct (bad timestamp, wrong scalar kind)
// must surface encoding/json's error, not be swallowed into a zero
// value.
func TestUnmarshalJSONFieldErrors(t *testing.T) {
	t.Parallel()
	if err := json.Unmarshal([]byte(`{"tag":"t","timestamp":123}`), &DataPoint{}); err == nil {
		t.Error("DataPoint: expected error for non-time timestamp")
	}
	if err := json.Unmarshal([]byte(`{"driver":5}`), &WriteCommand{}); err == nil {
		t.Error("WriteCommand: expected error for numeric driver field")
	}
	if err := json.Unmarshal([]byte(`[1,2]`), &DataPoint{}); err == nil {
		t.Error("DataPoint: expected error for array payload")
	}
	if err := json.Unmarshal([]byte(`"str"`), &WriteCommand{}); err == nil {
		t.Error("WriteCommand: expected error for string payload")
	}
}
