package parser

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestDefaultParser(t *testing.T) {
	p, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}

	dp := core.DataPoint{
		Driver:    "plc",
		Tag:       "temp",
		Value:     float32(23.5),
		Type:      core.TypeFloat32,
		Quality:   core.QualityGood,
		Timestamp: time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC),
	}
	payload, _ := json.Marshal(dp)

	got, err := p.Parse(payload, "")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if got.Driver != "plc" || got.Tag != "temp" {
		t.Errorf("unexpected: %+v", got)
	}
	// The declared type must survive the JSON round-trip: a float32
	// point decodes back as float32 (not the float64 default), so a
	// chained consumer sees exactly what the upstream driver produced.
	gotF, ok := got.Value.(float32)
	if !ok {
		t.Fatalf("value: expected float32, got %T (%v)", got.Value, got.Value)
	}
	if gotF != 23.5 {
		t.Errorf("value: expected 23.5, got %v", gotF)
	}
}

func TestDefaultParserFillsTimestamp(t *testing.T) {
	p, _ := New(nil)
	// JSON without timestamp
	payload := []byte(`{"driver":"d","tag":"t","value":1}`)

	got, err := p.Parse(payload, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestJSONPathParser(t *testing.T) {
	settings := map[string]any{
		"parser": map[string]any{
			"type":             "jsonpath",
			"driver":           "lora-gateway",
			"tag":              "{{ .payload.dev_id }}",
			"value":            "{{ .payload.temp }}",
			"data-type":        "float32",
			"group":            "sensors",
			"timestamp":        "{{ .payload.ts }}",
			"timestamp-format": "unix",
		},
	}
	p, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"dev_id":"sensor_01","temp":23.5,"ts":1694280000}`)
	got, err := p.Parse(payload, "lora/sensor_01/data")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if got.Driver != "lora-gateway" {
		t.Errorf("driver: expected lora-gateway, got %s", got.Driver)
	}
	if got.Tag != "sensor_01" {
		t.Errorf("tag: expected sensor_01, got %s", got.Tag)
	}
	if got.Group != "sensors" {
		t.Errorf("group: expected sensors, got %s", got.Group)
	}
	if v, ok := got.Value.(float64); !ok || v != 23.5 {
		t.Errorf("value: expected 23.5, got %v", got.Value)
	}
	if got.Type != core.TypeFloat32 {
		t.Errorf("type: expected float32, got %v", got.Type)
	}
	expectedTs := time.Unix(1694280000, 0)
	if !got.Timestamp.Equal(expectedTs) {
		t.Errorf("timestamp: expected %v, got %v", expectedTs, got.Timestamp)
	}
}

func TestRawParser(t *testing.T) {
	settings := map[string]any{
		"parser": map[string]any{
			"type":           "raw",
			"driver":         "factory",
			"tag-from-topic": uint64(2),
			"data-type":      "float32",
		},
	}
	p, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}

	// topic = sensor/factory/temp, payload = "23.5"
	got, err := p.Parse([]byte("23.5"), "sensor/factory/temp")
	if err != nil {
		t.Fatal(err)
	}

	if got.Driver != "factory" {
		t.Errorf("driver: expected factory, got %s", got.Driver)
	}
	if got.Tag != "factory" {
		t.Errorf("tag: expected 'factory' (segment 2), got %s", got.Tag)
	}
	if v, ok := got.Value.(float32); !ok || v != 23.5 {
		t.Errorf("value: expected 23.5, got %v", got.Value)
	}
}

func TestRawParserStaticTag(t *testing.T) {
	settings := map[string]any{
		"parser": map[string]any{
			"type":      "raw",
			"driver":    "dev",
			"tag":       "temperature",
			"data-type": "float64",
		},
	}
	p, _ := New(settings)

	got, err := p.Parse([]byte("42.5"), "any/topic")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "temperature" {
		t.Errorf("tag: expected temperature, got %s", got.Tag)
	}
	if v, ok := got.Value.(float64); !ok || v != 42.5 {
		t.Errorf("value: expected 42.5, got %v", got.Value)
	}
}

func TestUnknownParserType(t *testing.T) {
	settings := map[string]any{
		"parser": map[string]any{"type": "bogus"},
	}
	_, err := New(settings)
	if err == nil {
		t.Fatal("expected error for unknown parser type")
	}
}

// TestJSONPathParserInt64Precision guards the same corruption class as
// the core round-trip fix, on the inbound third-party path: gjson's
// .Value() coerces numbers to float64, which rounds integers above ±2^53
// when a chained core declares data-type int64.
func TestJSONPathParserInt64Precision(t *testing.T) {
	settings := map[string]any{
		"parser": map[string]any{
			"type":      "jsonpath",
			"driver":    "gw",
			"tag":       "{{ .payload.dev_id }}",
			"value":     "{{ .payload.counter }}",
			"data-type": "int64",
		},
	}
	p, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"dev_id":"meter_01","counter":9007199254740993}`)
	got, err := p.Parse(payload, "gw/meter_01/data")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	v, ok := got.Value.(int64)
	if !ok {
		t.Fatalf("expected int64, got %T (%v)", got.Value, got.Value)
	}
	if v != 9007199254740993 {
		t.Fatalf("silently corrupted: buffered 9007199254740993, parsed %d", v)
	}
}

// TestJSONPathParserUntypedKeepsGJSONTyping pins the fallback: without a
// configured data-type, values keep gjson's native typing (numbers →
// float64), matching the historical behavior for untyped configs.
func TestJSONPathParserUntypedKeepsGJSONTyping(t *testing.T) {
	settings := map[string]any{
		"parser": map[string]any{
			"type":   "jsonpath",
			"driver": "gw",
			"tag":    "{{ .payload.dev_id }}",
			"value":  "{{ .payload.counter }}",
		},
	}
	p, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"dev_id":"m1","counter":42}`)
	got, err := p.Parse(payload, "gw/m1/data")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if v, ok := got.Value.(float64); !ok || v != 42 {
		t.Fatalf("expected default float64(42), got %T (%v)", got.Value, got.Value)
	}
}

// TestRawParserInt64: the raw parser now parses int64/uint64 exactly from
// the payload digits (previously data-type int64 fell to the string
// default because parseScalar had no int64/uint64 cases).
func TestRawParserInt64(t *testing.T) {
	settings := map[string]any{
		"parser": map[string]any{
			"type":      "raw",
			"driver":    "dev",
			"tag":       "counter64",
			"data-type": "int64",
		},
	}
	p, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Parse([]byte("9007199254740993"), "any/topic")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if v, ok := got.Value.(int64); !ok || v != 9007199254740993 {
		t.Fatalf("expected int64(9007199254740993), got %T (%v)", got.Value, got.Value)
	}
}
