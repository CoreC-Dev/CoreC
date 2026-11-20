package engine

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestOfflineBuffer64BitRoundTrip verifies that 64-bit readings buffered
// during an outage replay bit-exactly: default float64 `any` decoding
// rounds integers beyond ±2^53; DataPoint.UnmarshalJSON's type-aware
// decode keeps the exact value.
func TestOfflineBuffer64BitRoundTrip(t *testing.T) {
	ob, err := NewOfflineBuffer(t.TempDir(), 10)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	points := []core.DataPoint{
		{Driver: "plc", Tag: "counter64", Value: int64(9007199254740993), Type: core.TypeInt64, Quality: core.QualityGood, Timestamp: time.Now()},
		{Driver: "plc", Tag: "neg64", Value: int64(-9007199254740993), Type: core.TypeInt64, Quality: core.QualityGood, Timestamp: time.Now()},
		{Driver: "plc", Tag: "u64max", Value: uint64(18446744073709551615), Type: core.TypeUint64, Quality: core.QualityGood, Timestamp: time.Now()},
		{Driver: "plc", Tag: "counter32", Value: int32(-2147483648), Type: core.TypeInt32, Quality: core.QualityGood, Timestamp: time.Now()},
	}
	if err := ob.Push(points, "mqtt"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	var replayed []core.DataPoint
	drained, err := ob.Drain("mqtt", func(got []core.DataPoint) error {
		replayed = got
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if drained != 1 {
		t.Fatalf("expected 1 drained batch, got %d", drained)
	}
	if len(replayed) != len(points) {
		t.Fatalf("expected %d replayed points, got %d", len(points), len(replayed))
	}

	for i, want := range []struct {
		tag  string
		want any
	}{
		{"counter64", int64(9007199254740993)},
		{"neg64", int64(-9007199254740993)},
		{"u64max", uint64(18446744073709551615)},
		{"counter32", int32(-2147483648)},
	} {
		got := replayed[i]
		if got.Tag != want.tag {
			t.Errorf("point %d: tag %q, want %q", i, got.Tag, want.tag)
		}
		if got.Value != want.want {
			t.Errorf("%s: silently corrupted — buffered %v (%T), replayed %v (%T)",
				want.tag, want.want, want.want, got.Value, got.Value)
		}
	}
}

// TestOfflineBufferUnTypedValuesStayUnchanged verifies the fallback:
// values whose payload has no usable type/value pairing (absent "type"
// key, or a token shape that does not match the declared type) keep the
// plain `any` decode.
func TestOfflineBufferUnTypedValuesStayUnchanged(t *testing.T) {
	ob, err := NewOfflineBuffer(t.TempDir(), 10)
	if err != nil {
		t.Fatalf("NewOfflineBuffer: %v", err)
	}
	defer ob.Close()

	// Type omitted → DataType zero (TypeBool); a number token with a
	// bool-typed decode falls back to the default float64.
	points := []core.DataPoint{
		{Driver: "plc", Tag: "legacy", Value: 42, Timestamp: time.Now()},
	}
	if err := ob.Push(points, "mqtt"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	var replayed []core.DataPoint
	if _, err := ob.Drain("mqtt", func(got []core.DataPoint) error {
		replayed = got
		return nil
	}); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if v, ok := replayed[0].Value.(float64); !ok || v != 42 {
		t.Errorf("untyped value should keep the default float64 decode, got %T (%v)", replayed[0].Value, replayed[0].Value)
	}
}
