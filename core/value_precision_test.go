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
