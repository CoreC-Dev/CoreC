package s7

import (
	"encoding/binary"
	"fmt"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/robinson/gos7"
)

func decodeS7Buffer(buf []byte, addr s7Address, dt core.DataType, h *gos7.Helper) (any, error) {
	if addr.isBit {
		return h.GetBoolAt(buf[0], addr.bit), nil
	}

	// Verify the buffer is large enough for the requested data type. A
	// mismatch between the address kind and the configured type (e.g. a
	// DBB0 byte address configured with type uint16, yielding a 1-byte
	// buffer) would otherwise cause an index-out-of-range panic inside the
	// decoding helpers below. Convert that into a graceful error.
	requiredSize := s7RequiredSize(dt)
	if requiredSize > 0 && len(buf) < requiredSize {
		return nil, fmt.Errorf("s7: buffer too small for type %s: have %d bytes, need %d", dt, len(buf), requiredSize)
	}

	return decodeS7TypedValue(buf, addr, dt, h)
}

// s7RequiredSize returns the minimum byte length needed to decode a value of
// the given type, or 0 for types with no fixed size requirement.
func s7RequiredSize(dt core.DataType) int {
	switch dt {
	case core.TypeBool, core.TypeUint8, core.TypeInt8, core.TypeString:
		return 1
	case core.TypeUint16, core.TypeInt16:
		return 2
	case core.TypeUint32, core.TypeInt32, core.TypeFloat32:
		return 4
	case core.TypeFloat64:
		return 8
	default:
		return 0
	}
}

// decodeS7TypedValue decodes a fixed-size value from buf according to dt.
func decodeS7TypedValue(buf []byte, addr s7Address, dt core.DataType, h *gos7.Helper) (any, error) {
	switch dt {
	case core.TypeBool:
		return h.GetBoolAt(buf[0], addr.bit), nil
	case core.TypeUint8:
		return buf[0], nil
	case core.TypeInt8:
		return int8(buf[0]), nil
	case core.TypeUint16:
		return binary.BigEndian.Uint16(buf), nil
	case core.TypeInt16:
		return int16(binary.BigEndian.Uint16(buf)), nil
	case core.TypeUint32:
		return binary.BigEndian.Uint32(buf), nil
	case core.TypeInt32:
		return int32(binary.BigEndian.Uint32(buf)), nil
	case core.TypeFloat32:
		return h.GetRealAt(buf, 0), nil
	case core.TypeFloat64:
		return h.GetLRealAt(buf, 0), nil
	case core.TypeString:
		return h.GetStringAt(buf, 0), nil
	default:
		// Unsupported types (int64, uint64, bytes, and any unknown type) must
		// not silently return a wrong value. Surface an explicit error instead.
		return nil, fmt.Errorf("s7: unsupported read data type: %s", dt)
	}
}

func encodeS7Value(v any, addr s7Address, dt core.DataType, h *gos7.Helper) ([]byte, error) {
	buf := make([]byte, addr.size)
	switch dt {
	case core.TypeBool:
		if b, ok := v.(bool); ok && b {
			buf[0] = 1
		}
	case core.TypeUint8, core.TypeInt8:
		buf[0] = byte(util.ToUint16(v))
	case core.TypeUint16, core.TypeInt16:
		binary.BigEndian.PutUint16(buf, util.ToUint16(v))
	case core.TypeUint32, core.TypeInt32:
		binary.BigEndian.PutUint32(buf, util.ToUint32(v))
	case core.TypeUint64, core.TypeInt64:
		binary.BigEndian.PutUint64(buf, util.ToUint64(v))
	case core.TypeFloat32:
		h.SetRealAt(buf, 0, util.ToFloat32(v))
	case core.TypeFloat64:
		h.SetLRealAt(buf, 0, util.ToFloat64(v))
	case core.TypeBytes:
		b, ok := v.([]byte)
		if !ok {
			return nil, fmt.Errorf("s7: bytes write requires []byte value, got %T", v)
		}
		copy(buf, b)
	case core.TypeString:
		// S7 strings use a PLC-specific header layout (max-len/actual-len prefix)
		// that this driver cannot encode safely without the matching gos7 helper.
		// Refuse rather than silently writing a malformed value.
		return nil, fmt.Errorf("s7: writing string is not supported")
	default:
		return nil, fmt.Errorf("s7: unsupported write data type: %s", dt)
	}
	return buf, nil
}
