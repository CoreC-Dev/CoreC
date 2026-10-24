package driverbase

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestWriteValueForTag verifies the write-side inverse of the read path's
// scale/offset transform. Reads report engineering
// values via util.ApplyTransform (raw×scale + offset); writes carrying an
// engineering value must be converted back to the raw representation
// before encoding, or a scaled tag receives a value off by its scale
// factor. WriteValueForTag is the single shared helper all three southbound
// drivers route through, so its semantics are pinned here once.
func TestWriteValueForTag(t *testing.T) {
	// Default tag (no transform configured): value passes through with
	// the original type preserved.
	t.Run("untransformed tag passes through", func(t *testing.T) {
		cmd := core.WriteCommand{Tag: "t", Value: uint16(150)}
		cfg := core.TagConfig{Name: "t", Scale: 0, Offset: 0}
		got := WriteValueForTag(cmd, cfg)
		if got.(uint16) != 150 {
			t.Errorf("got %v (%T), want uint16 150", got, got)
		}
	})

	// Scaled tag: engineering value is inverted to the raw representation.
	// pressure reads raw 150 with scale 0.01 as 1.5; writing 1.5 must
	// encode raw 150 (previously it encoded 1).
	t.Run("scaled tag inverts engineering value", func(t *testing.T) {
		cmd := core.WriteCommand{Tag: "pressure", Value: 1.5}
		cfg := core.TagConfig{Name: "pressure", Scale: 0.01, Offset: 0}
		got := WriteValueForTag(cmd, cfg)
		if got.(float64) != 150 {
			t.Errorf("got %v, want 150", got)
		}
	})

	// Scale + offset: raw = (value - offset) / scale.
	t.Run("scale and offset invert", func(t *testing.T) {
		cmd := core.WriteCommand{Tag: "t", Value: float64(15)}
		cfg := core.TagConfig{Name: "t", Scale: 0.1, Offset: 5}
		got := WriteValueForTag(cmd, cfg)
		if got.(float64) != 100 {
			t.Errorf("got %v, want 100", got)
		}
	})

	// Raw escape hatch: the operator explicitly wants the device-side
	// representation written as-is, bypassing the inverse transform.
	t.Run("raw bypasses inversion", func(t *testing.T) {
		cmd := core.WriteCommand{Tag: "pressure", Value: uint16(150), Raw: true}
		cfg := core.TagConfig{Name: "pressure", Scale: 0.01, Offset: 0}
		got := WriteValueForTag(cmd, cfg)
		if got.(uint16) != 150 {
			t.Errorf("got %v (%T), want uint16 150 untouched", got, got)
		}
	})

	// Non-numeric values are never scaled on reads; writes pass them
	// through unchanged so bool/string tags round-trip.
	t.Run("bool passes through", func(t *testing.T) {
		cmd := core.WriteCommand{Tag: "pump_on", Value: true}
		cfg := core.TagConfig{Name: "pump_on", Scale: 0.01, Offset: 0}
		got := WriteValueForTag(cmd, cfg)
		if got.(bool) != true {
			t.Errorf("got %v, want true", got)
		}
	})
}
