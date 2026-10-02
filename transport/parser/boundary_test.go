package parser

import (
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TEST-007: boundary and error-path tests for parseScalar and parseTimestamp
// to raise parser coverage from 66.2% to ≥85%.

func TestParseScalarBool(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"yes", false},
		{"", false},
	}
	for _, c := range cases {
		if got := parseScalar(c.s, core.TypeBool); got != c.want {
			t.Errorf("parseScalar(%q, TypeBool) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestParseScalarIntegers(t *testing.T) {
	// Valid values.
	if got := parseScalar("100", core.TypeUint16); got != uint16(100) {
		t.Errorf("uint16 100: got %v", got)
	}
	if got := parseScalar("-50", core.TypeInt16); got != int16(-50) {
		t.Errorf("int16 -50: got %v", got)
	}
	if got := parseScalar("70000", core.TypeUint32); got != uint32(70000) {
		t.Errorf("uint32 70000: got %v", got)
	}
	if got := parseScalar("-200000", core.TypeInt32); got != int32(-200000) {
		t.Errorf("int32 -200000: got %v", got)
	}

	// Overflow / invalid → zero value.
	if got := parseScalar("70000", core.TypeUint16); got != uint16(0) {
		t.Errorf("uint16 overflow: got %v, want 0", got)
	}
	if got := parseScalar("abc", core.TypeInt16); got != int16(0) {
		t.Errorf("int16 invalid: got %v, want 0", got)
	}
	if got := parseScalar("", core.TypeUint32); got != uint32(0) {
		t.Errorf("uint32 empty: got %v, want 0", got)
	}
}

func TestParseScalarFloats(t *testing.T) {
	if got := parseScalar("3.14", core.TypeFloat32); got != float32(3.14) {
		t.Errorf("float32 3.14: got %v", got)
	}
	if got := parseScalar("2.71828", core.TypeFloat64); got != 2.71828 {
		t.Errorf("float64: got %v", got)
	}
	// Invalid → zero.
	if got := parseScalar("nan-text", core.TypeFloat32); got != float32(0) {
		t.Errorf("float32 invalid: got %v, want 0", got)
	}
	if got := parseScalar("", core.TypeFloat64); got != float64(0) {
		t.Errorf("float64 empty: got %v, want 0", got)
	}
}

func TestParseScalarStringPassthrough(t *testing.T) {
	// Unknown/default type returns the raw string.
	if got := parseScalar("hello", core.TypeString); got != "hello" {
		t.Errorf("string passthrough: got %v", got)
	}
	if got := parseScalar("123abc", core.TypeString); got != "123abc" {
		t.Errorf("string passthrough: got %v", got)
	}
}

func TestParseTimestampUnix(t *testing.T) {
	got := parseTimestamp("1700000000", "unix")
	if got.IsZero() {
		t.Error("expected non-zero time for unix timestamp")
	}
	if got.Unix() != 1700000000 {
		t.Errorf("unix: got %d, want 1700000000", got.Unix())
	}
}

func TestParseTimestampUnixMilli(t *testing.T) {
	got := parseTimestamp("1700000000000", "unixmilli")
	if got.IsZero() {
		t.Error("expected non-zero time for unixmilli timestamp")
	}
	if got.UnixMilli() != 1700000000000 {
		t.Errorf("unixmilli: got %d, want 1700000000000", got.UnixMilli())
	}
}

func TestParseTimestampRFC3339(t *testing.T) {
	got := parseTimestamp("2023-11-14T22:13:20Z", "")
	if got.IsZero() {
		t.Error("expected non-zero time for RFC3339")
	}
	want := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("RFC3339: got %v, want %v", got, want)
	}
}

func TestParseTimestampRFC3339Nano(t *testing.T) {
	got := parseTimestamp("2023-11-14T22:13:20.123456789Z", "")
	if got.IsZero() {
		t.Error("expected non-zero time for RFC3339Nano")
	}
}

func TestParseTimestampEdgeCases(t *testing.T) {
	// Empty string → zero time.
	if got := parseTimestamp("", "unix"); !got.IsZero() {
		t.Error("empty string should yield zero time")
	}
	// Invalid unix → zero time.
	if got := parseTimestamp("not-a-number", "unix"); !got.IsZero() {
		t.Error("invalid unix should yield zero time")
	}
	// Invalid unixmilli → zero time.
	if got := parseTimestamp("abc", "unixmilli"); !got.IsZero() {
		t.Error("invalid unixmilli should yield zero time")
	}
	// Invalid RFC3339 → zero time.
	if got := parseTimestamp("not-a-date", ""); !got.IsZero() {
		t.Error("invalid RFC3339 should yield zero time")
	}
	// Case-insensitive format: "UNIX" should work.
	got := parseTimestamp("1700000000", "UNIX")
	if got.IsZero() {
		t.Error("expected case-insensitive format matching")
	}
}

// ─── parseTplValueString tests ─────────────────────────────────────

func TestParseTplValueString(t *testing.T) {
	cases := []struct {
		input string
		want  any
	}{
		{"true", true},
		{"false", false},
		{"3.14", 3.14},
		{"42", 42.0},
		{"hello", "hello"},
		{"  true  ", true}, // trimmed
		{"  1.5  ", 1.5},   // trimmed
		{"", ""},
	}
	for _, c := range cases {
		if got := parseTplValueString(c.input); got != c.want {
			t.Errorf("parseTplValueString(%q) = %v (%T), want %v (%T)", c.input, got, got, c.want, c.want)
		}
	}
}

// ─── jsonPathField.resolve / resolveValue tests ────────────────────

func TestJsonPathFieldResolveStatic(t *testing.T) {
	f := jsonPathField{isStatic: true, static: "fixed"}
	if got := f.resolve(nil, "topic"); got != "fixed" {
		t.Errorf("static resolve: got %q, want fixed", got)
	}
}

func TestJsonPathFieldResolveTopic(t *testing.T) {
	f := jsonPathField{isTopic: true}
	if got := f.resolve(nil, "my/topic"); got != "my/topic" {
		t.Errorf("topic resolve: got %q", got)
	}
}

func TestJsonPathFieldResolveGjson(t *testing.T) {
	f := jsonPathField{gjsonPath: "value"}
	payload := []byte(`{"value": 42}`)
	if got := f.resolve(payload, ""); got != "42" {
		t.Errorf("gjson resolve: got %q, want 42", got)
	}
}

func TestJsonPathFieldResolveEmpty(t *testing.T) {
	f := jsonPathField{}
	if got := f.resolve(nil, ""); got != "" {
		t.Errorf("empty resolve: got %q", got)
	}
}

func TestJsonPathFieldResolveValueStatic(t *testing.T) {
	f := jsonPathField{isStatic: true, static: "3.14"}
	if got := f.resolveValue(nil, ""); got != 3.14 {
		t.Errorf("static resolveValue: got %v, want 3.14", got)
	}
}

func TestJsonPathFieldResolveValueTopic(t *testing.T) {
	f := jsonPathField{isTopic: true}
	if got := f.resolveValue(nil, "t/x"); got != "t/x" {
		t.Errorf("topic resolveValue: got %v", got)
	}
}

func TestJsonPathFieldResolveValueBool(t *testing.T) {
	f := jsonPathField{gjsonPath: "flag"}
	payload := []byte(`{"flag": true}`)
	if got := f.resolveValue(payload, ""); got != true {
		t.Errorf("bool resolveValue: got %v, want true", got)
	}
}
