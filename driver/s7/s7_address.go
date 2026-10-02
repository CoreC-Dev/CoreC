package s7

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
)

// ============================================================
// Address parser & decoding
// ============================================================

// Regular expressions for S7 address syntax
var (
	// DB1.DBX0.0, DB100.DBD4, DB5.DBW2, DB1.DBB0
	reDB = regexp.MustCompile(`(?i)^DB(\d+)\.DB([XBWD])(\d+)(?:\.(\d+))?$`)
	// M0.0, MB10, MW20, MD30
	reM = regexp.MustCompile(`(?i)^M([BWD]?)(\d+)(?:\.(\d+))?$`)
	// I0.0, IB1, IW2, ID4, or E0.0, EB1, EW2, ED4
	reI = regexp.MustCompile(`(?i)^[IE]([BWD]?)(\d+)(?:\.(\d+))?$`)
	// Q0.0, QB1, QW2, QD4, or A0.0, AB1, AW2, AD4
	reQ = regexp.MustCompile(`(?i)^[QA]([BWD]?)(\d+)(?:\.(\d+))?$`)
)

func parseS7Address(addrStr string, dt core.DataType) (s7Address, error) {
	addrStr = strings.TrimSpace(addrStr)

	// Try DB pattern
	if m := reDB.FindStringSubmatch(addrStr); len(m) > 0 {
		dbNum, _ := strconv.Atoi(m[1])
		kind := strings.ToUpper(m[2])
		byteOffset, _ := strconv.Atoi(m[3])

		bitOffset := 0
		if m[4] != "" {
			bitOffset, _ = strconv.Atoi(m[4])
		}

		size := dataSizeForKind(kind, dt)
		return s7Address{
			area:     areaDB,
			dbNumber: dbNum,
			start:    byteOffset,
			bit:      uint(bitOffset),
			size:     size,
			isBit:    kind == "X",
		}, nil
	}

	// Try Merker (M) pattern
	if m := reM.FindStringSubmatch(addrStr); len(m) > 0 {
		kind := strings.ToUpper(m[1])
		byteOffset, _ := strconv.Atoi(m[2])
		bitOffset := 0
		isBit := false
		if m[3] != "" {
			bitOffset, _ = strconv.Atoi(m[3])
			isBit = true
		} else if kind == "" {
			isBit = (dt == core.TypeBool)
		}

		size := dataSizeForKind(kind, dt)
		return s7Address{
			area:  areaM,
			start: byteOffset,
			bit:   uint(bitOffset),
			size:  size,
			isBit: isBit,
		}, nil
	}

	// Try Input (I/E) pattern
	if m := reI.FindStringSubmatch(addrStr); len(m) > 0 {
		kind := strings.ToUpper(m[1])
		byteOffset, _ := strconv.Atoi(m[2])
		bitOffset := 0
		isBit := false
		if m[3] != "" {
			bitOffset, _ = strconv.Atoi(m[3])
			isBit = true
		} else if kind == "" {
			isBit = (dt == core.TypeBool)
		}

		size := dataSizeForKind(kind, dt)
		return s7Address{
			area:  areaI,
			start: byteOffset,
			bit:   uint(bitOffset),
			size:  size,
			isBit: isBit,
		}, nil
	}

	// Try Output (Q/A) pattern
	if m := reQ.FindStringSubmatch(addrStr); len(m) > 0 {
		kind := strings.ToUpper(m[1])
		byteOffset, _ := strconv.Atoi(m[2])
		bitOffset := 0
		isBit := false
		if m[3] != "" {
			bitOffset, _ = strconv.Atoi(m[3])
			isBit = true
		} else if kind == "" {
			isBit = (dt == core.TypeBool)
		}

		size := dataSizeForKind(kind, dt)
		return s7Address{
			area:  areaQ,
			start: byteOffset,
			bit:   uint(bitOffset),
			size:  size,
			isBit: isBit,
		}, nil
	}

	return s7Address{}, fmt.Errorf("unrecognized s7 address format: %s", addrStr)
}

func dataSizeForKind(kind string, dt core.DataType) int {
	switch kind {
	case "X":
		return 1 // Read full byte for bit
	case "B":
		return 1
	case "W":
		return 2
	case "D":
		return 4
	default:
		// Infer from DataType
		switch dt {
		case core.TypeBool:
			return 1
		case core.TypeInt8, core.TypeUint8:
			return 1
		case core.TypeInt16, core.TypeUint16:
			return 2
		case core.TypeInt32, core.TypeUint32, core.TypeFloat32:
			return 4
		case core.TypeInt64, core.TypeUint64, core.TypeFloat64:
			return 8
		default:
			return 2
		}
	}
}
