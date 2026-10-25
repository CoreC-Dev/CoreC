package core

import (
	"encoding/json"
	"strconv"
)

// decodeValueByType reconstructs a DataPoint/WriteCommand value from its
// raw JSON token using the declared DataType, restoring the exact Go type
// the driver originally produced. This exists because encoding/json
// decodes every JSON number into float64 when the target is `any` — which
// silently rounds any integer above ±2^53 (9007199254740993 becomes
// 9007199254740992). That rounding corrupted 64-bit readings on every
// round-trip through these structs: offline-buffer replay, chained-core
// inbound parsing, MQTT commands and API writes.
//
// Fallback rule: when the token does not match the declared type's
// expected JSON form, the token decodes with the default `any` rules
// (numbers → float64), preserving the pre-existing behavior for
// untyped or malformed payloads.
func decodeValueByType(raw json.RawMessage, dt DataType) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	text := string(raw)
	switch dt {
	case TypeInt8, TypeInt16, TypeInt32, TypeInt64:
		return decodeSignedToken(text, dt)
	case TypeUint8, TypeUint16, TypeUint32, TypeUint64:
		return decodeUnsignedToken(text, dt)
	case TypeFloat32, TypeFloat64:
		return decodeFloatToken(text, dt)
	case TypeBool:
		return decodeBoolToken(text)
	case TypeString:
		return decodeStringToken(text)
	case TypeBytes:
		return decodeBytesToken(text)
	}
	return decodeDefaultAny([]byte(text))
}

// isNumberToken reports whether the raw JSON token is a number literal
// (digit or '-' lead), as opposed to a string, bool, null or object.
func isNumberToken(text string) bool {
	return text[0] == '-' || (text[0] >= '0' && text[0] <= '9')
}

// decodeSignedToken parses a number token exactly through ParseInt —
// never through float64 — and narrows to the declared width. Non-number
// or unparseable tokens fall back to the default `any` decode.
func decodeSignedToken(text string, dt DataType) any {
	if !isNumberToken(text) {
		return decodeDefaultAny([]byte(text))
	}
	v, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return decodeDefaultAny([]byte(text))
	}
	switch dt {
	case TypeInt8:
		return int8(v)
	case TypeInt16:
		return int16(v)
	case TypeInt32:
		return int32(v)
	default:
		return v
	}
}

// decodeUnsignedToken is decodeSignedToken for the unsigned widths.
func decodeUnsignedToken(text string, dt DataType) any {
	if !isNumberToken(text) {
		return decodeDefaultAny([]byte(text))
	}
	v, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return decodeDefaultAny([]byte(text))
	}
	switch dt {
	case TypeUint8:
		return uint8(v)
	case TypeUint16:
		return uint16(v)
	case TypeUint32:
		return uint32(v)
	default:
		return v
	}
}

// decodeFloatToken parses a number token at the declared float width.
func decodeFloatToken(text string, dt DataType) any {
	if !isNumberToken(text) {
		return decodeDefaultAny([]byte(text))
	}
	if dt == TypeFloat32 {
		if v, err := strconv.ParseFloat(text, 32); err == nil {
			return float32(v)
		}
	} else if v, err := strconv.ParseFloat(text, 64); err == nil {
		return v
	}
	return decodeDefaultAny([]byte(text))
}

// decodeBoolToken accepts only the true/false literals.
func decodeBoolToken(text string) any {
	if text == "true" || text == "false" {
		return text == "true"
	}
	return decodeDefaultAny([]byte(text))
}

// decodeStringToken accepts only string tokens for TypeString.
func decodeStringToken(text string) any {
	if text != "" && text[0] == '"' {
		var s string
		if json.Unmarshal([]byte(text), &s) == nil {
			return s
		}
	}
	return decodeDefaultAny([]byte(text))
}

// decodeBytesToken accepts only base64 string tokens for TypeBytes, the
// form encoding/json produces when marshaling []byte.
func decodeBytesToken(text string) any {
	if text != "" && text[0] == '"' {
		var b []byte
		if json.Unmarshal([]byte(text), &b) == nil {
			return b
		}
	}
	return decodeDefaultAny([]byte(text))
}

// decodeDefaultAny is the plain `any` decode — the pre-fix behavior for
// token shapes that do not match the declared type.
func decodeDefaultAny(raw []byte) any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}

// probeRawValue extracts the raw JSON token of the "value" field without
// interpreting it. The second pass only materializes the one key, so the
// cost is a single extra scan of the (small) payload document.
func probeRawValue(data []byte) json.RawMessage {
	var probe struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil
	}
	return probe.Value
}

// UnmarshalJSON decodes a DataPoint and then rebuilds Value with the
// exact Go type its Type names, so numeric values survive the JSON
// round-trip bit-exactly (see decodeValueByType).
func (dp *DataPoint) UnmarshalJSON(data []byte) error {
	type dataPointFields DataPoint // alias: same fields, no inherited methods
	var fields dataPointFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	raw := probeRawValue(data)
	v := decodeValueByType(raw, fields.Type)
	*dp = DataPoint(fields)
	dp.Value = v
	return nil
}

// WriteCommand represents a command to write a value to a device.
type WriteCommand struct {
	Driver string   `json:"driver"`
	Device string   `json:"device"`
	Tag    string   `json:"tag"`
	Value  any      `json:"value"`
	Type   DataType `json:"type"`

	// Source records where the command originated: "api" for direct
	// HTTP writes (POST /write) and "command" for reverse-path commands
	// arriving via a transport command channel. It is stamped by the
	// trust boundary (the route handler / the engine command listener),
	// never trusted from client payloads, so the write audit history
	// cannot be mislabeled by an external sender. Omitted when empty.
	Source string `json:"source,omitempty"`

	// Remote records the network address of the caller for
	// API-originated writes (r.RemoteAddr()). Reverse-path commands have
	// no meaningful peer address and leave it empty. Omitted when empty.
	Remote string `json:"remote,omitempty"`

	// Raw requests a raw-device-value write: the driver skips the inverse
	// of the read path's scale/offset transform and encodes the command
	// value as-is. By default (Raw=false) a write carrying the
	// engineering value is converted back to the raw representation so
	// reads and writes round-trip for scaled tags.
	// Use Raw=true for commissioning flows that speak the device's own
	// units. Omitted when false.
	Raw bool `json:"raw,omitempty"`
}

// UnmarshalJSON decodes a WriteCommand and then rebuilds Value with the
// exact Go type its Type names, so 64-bit write commands survive the
// JSON decode bit-exactly (see decodeValueByType) — a cloud-issued
// int64 setpoint above ±2^53 previously decoded as a rounded float64
// and the PLC received the wrong register payload.
func (cmd *WriteCommand) UnmarshalJSON(data []byte) error {
	type writeCommandFields WriteCommand // alias: same fields, no inherited methods
	var fields writeCommandFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	raw := probeRawValue(data)
	v := decodeValueByType(raw, fields.Type)
	*cmd = WriteCommand(fields)
	cmd.Value = v
	return nil
}
