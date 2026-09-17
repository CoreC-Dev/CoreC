// Package parser provides pluggable parsing of incoming transport payloads
// (MQTT messages, HTTP webhook bodies) into core.DataPoint values.
//
// Different transport sources speak different wire formats, so different
// parsers are needed.
// The "default" parser handles CoreC→CoreC chaining (both sides share the
// same DataPoint JSON schema); "jsonpath" maps arbitrary JSON fields onto
// DataPoint via configurable paths; "raw" treats the entire payload as
// a scalar value.
package parser

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/tidwall/gjson"
)

// Parser converts a raw payload (and optional MQTT topic for context) into
// a DataPoint.  Parse is called once per incoming message.
type Parser interface {
	Parse(payload []byte, topic string) (core.DataPoint, error)
}

// New creates a Parser from a settings map.  Recognised keys:
//
//	parser.type        "default" | "jsonpath" | "raw"  (default: "default")
//	parser.driver      static string for driver field
//	parser.tag         path or static string
//	parser.value       path (jsonpath) — for "raw" ignored
//	parser.type-field  path or static for DataType
//	parser.group       static string
//	parser.device      static string
//	parser.timestamp   path
//	parser.timestamp-format  "rfc3339" | "unix" | "unixmilli"  (default: "rfc3339")
//
// When parser.type is absent or "default", the parser is a straight
// json.Unmarshal into DataPoint — the zero-cost path for CoreC→CoreC.
//
// For "jsonpath", field values use Go template syntax (backward-compatible):
//
//	"{{ .payload.dev_id }}"  →  gjson path "dev_id"
//	"{{ .topic }}"           →  the MQTT topic string
//	"static-string"          →  literal value
func New(settings map[string]any) (Parser, error) {
	parserSettings := getMapSetting(settings, "parser")
	ptype := getStringSetting(parserSettings, "type", "default")

	switch strings.ToLower(ptype) {
	case "default", "":
		return &defaultParser{}, nil
	case "jsonpath":
		return newJSONPathParser(parserSettings)
	case "raw":
		return newRawParser(parserSettings)
	default:
		return nil, fmt.Errorf("parser: unknown type %q", ptype)
	}
}

// --- defaultParser: json.Unmarshal(DataPoint) ---

type defaultParser struct{}

func (p *defaultParser) Parse(payload []byte, _ string) (core.DataPoint, error) {
	var dp core.DataPoint
	if err := json.Unmarshal(payload, &dp); err != nil {
		return core.DataPoint{}, fmt.Errorf("default parser: %w", err)
	}
	if dp.Timestamp.IsZero() {
		dp.Timestamp = time.Now()
	}
	return dp, nil
}

// --- jsonpathParser: field mapping via gjson ---

// jsonPathField represents a configurable field that can be either:
//   - a static string literal
//   - a gjson path into the payload JSON (extracted from "{{ .payload.X }}" syntax)
//   - a reference to the MQTT topic ("{{ .topic }}")
type jsonPathField struct {
	static    string
	gjsonPath string
	isTopic   bool
	isStatic  bool
}

func parseField(s string) jsonPathField {
	s = strings.TrimSpace(s)
	// Check for template syntax {{ .payload.X }}
	if strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") {
		inner := strings.TrimSpace(s[2 : len(s)-2])
		if inner == ".topic" {
			return jsonPathField{isTopic: true}
		}
		// Strip ".payload." prefix to get the gjson path
		const prefix = ".payload."
		if strings.HasPrefix(inner, prefix) {
			return jsonPathField{gjsonPath: inner[len(prefix):]}
		}
		// Unknown template expression — treat as static
		return jsonPathField{static: s, isStatic: true}
	}
	return jsonPathField{static: s, isStatic: true}
}

func (f jsonPathField) resolve(payload []byte, topic string) string {
	if f.isStatic {
		return f.static
	}
	if f.isTopic {
		return topic
	}
	if f.gjsonPath != "" {
		return gjson.GetBytes(payload, f.gjsonPath).String()
	}
	return ""
}

// resolveValue is like resolve but preserves the original JSON type
// (number, bool, string) instead of always returning a string.
func (f jsonPathField) resolveValue(payload []byte, topic string) any {
	if f.isStatic {
		return parseTplValueString(f.static)
	}
	if f.isTopic {
		return topic
	}
	if f.gjsonPath != "" {
		r := gjson.GetBytes(payload, f.gjsonPath)
		switch {
		case r.Type == gjson.JSON:
			return r.String()
		case r.IsBool():
			return r.Bool()
		default:
			// Numbers, strings, and nulls are handled via .Value()
			return r.Value()
		}
	}
	return ""
}

type jsonPathParser struct {
	driver         string
	tagField       jsonPathField
	valueField     jsonPathField
	typeStr        string
	group          string
	device         string
	timestampField jsonPathField
	timestampFmt   string
}

func newJSONPathParser(s map[string]any) (*jsonPathParser, error) {
	p := &jsonPathParser{
		driver:         getStringSetting(s, "driver", ""),
		typeStr:        getStringSetting(s, "data-type", ""),
		group:          getStringSetting(s, "group", ""),
		device:         getStringSetting(s, "device", ""),
		timestampFmt:   getStringSetting(s, "timestamp-format", "rfc3339"),
		tagField:       parseField(getStringSetting(s, "tag", "")),
		valueField:     parseField(getStringSetting(s, "value", "")),
		timestampField: parseField(getStringSetting(s, "timestamp", "")),
	}
	return p, nil
}

func (p *jsonPathParser) Parse(payload []byte, topic string) (core.DataPoint, error) {
	// Validate JSON (gjson handles parsing lazily, but we check validity)
	if !gjson.ValidBytes(payload) {
		return core.DataPoint{}, fmt.Errorf("jsonpath parser: invalid JSON")
	}

	dp := core.DataPoint{
		Driver: p.driver,
		Group:  p.group,
		Device: p.device,
	}

	dp.Tag = p.tagField.resolve(payload, topic)
	dp.Value = p.valueField.resolveValue(payload, topic)

	if p.typeStr != "" {
		dt, _ := core.ParseDataType(p.typeStr)
		dp.Type = dt
	}

	// Timestamp
	tsStr := p.timestampField.resolve(payload, topic)
	if tsStr != "" {
		dp.Timestamp = parseTimestamp(tsStr, p.timestampFmt)
	}
	if dp.Timestamp.IsZero() {
		dp.Timestamp = time.Now()
	}

	dp.Quality = core.QualityGood
	return dp, nil
}

// --- rawParser: payload is the value, metadata from config/topic ---

type rawParser struct {
	driver       string
	group        string
	device       string
	typeStr      string
	tagFromTopic int // 0 = use static tag, N = use Nth segment of topic
	tag          string
	timestampFmt string
}

func newRawParser(s map[string]any) (*rawParser, error) {
	return &rawParser{
		driver:       getStringSetting(s, "driver", ""),
		group:        getStringSetting(s, "group", ""),
		device:       getStringSetting(s, "device", ""),
		typeStr:      getStringSetting(s, "data-type", "float64"),
		tag:          getStringSetting(s, "tag", ""),
		tagFromTopic: util.GetIntSetting(s, "tag-from-topic", 0),
		timestampFmt: getStringSetting(s, "timestamp-format", "rfc3339"),
	}, nil
}

func (p *rawParser) Parse(payload []byte, topic string) (core.DataPoint, error) {
	dp := core.DataPoint{
		Driver:    p.driver,
		Group:     p.group,
		Device:    p.device,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}

	// Determine tag
	if p.tagFromTopic > 0 {
		segs := strings.Split(topic, "/")
		if p.tagFromTopic <= len(segs) {
			dp.Tag = segs[p.tagFromTopic-1]
		}
	} else {
		dp.Tag = p.tag
	}

	// Parse value based on type
	valStr := strings.TrimSpace(string(payload))
	dt, _ := core.ParseDataType(p.typeStr)
	dp.Type = dt
	dp.Value = parseScalar(valStr, dt)

	return dp, nil
}

// ============================================================
// Helpers
// ============================================================

func getMapSetting(settings map[string]any, key string) map[string]any {
	if v, ok := settings[key].(map[string]any); ok {
		return v
	}
	return nil
}

func getStringSetting(s map[string]any, key, def string) string {
	if s == nil {
		return def
	}
	if v, ok := s[key].(string); ok {
		return v
	}
	return def
}

// parseTplValueString tries to preserve the original JSON type from a
// static string value (for backward compatibility with non-template configs).
func parseTplValueString(s string) any {
	s = strings.TrimSpace(s)
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

func parseTimestamp(s, format string) time.Time {
	if s == "" {
		return time.Time{}
	}
	switch strings.ToLower(format) {
	case "unix":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return time.Unix(int64(f), 0)
		}
	case "unixmilli":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return time.UnixMilli(int64(f))
		}
	default: // rfc3339
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func parseScalar(s string, dt core.DataType) any {
	switch dt {
	case core.TypeBool:
		return s == "true" || s == "1"
	case core.TypeUint16:
		if v, err := strconv.ParseUint(s, 10, 16); err == nil {
			return uint16(v)
		}
		return uint16(0)
	case core.TypeInt16:
		if v, err := strconv.ParseInt(s, 10, 16); err == nil {
			return int16(v)
		}
		return int16(0)
	case core.TypeUint32:
		if v, err := strconv.ParseUint(s, 10, 32); err == nil {
			return uint32(v)
		}
		return uint32(0)
	case core.TypeInt32:
		if v, err := strconv.ParseInt(s, 10, 32); err == nil {
			return int32(v)
		}
		return int32(0)
	case core.TypeFloat32:
		if v, err := strconv.ParseFloat(s, 32); err == nil {
			return float32(v)
		}
		return float32(0)
	case core.TypeFloat64:
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			return v
		}
		return float64(0)
	default:
		return s
	}
}
