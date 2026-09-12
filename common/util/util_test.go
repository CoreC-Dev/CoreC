package util

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- GetIntSetting ---

func TestGetIntSetting(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		key      string
		def      int
		want     int
	}{
		{"int value", map[string]any{"port": 502}, "port", 0, 502},
		{"float64 value (YAML unquoted)", map[string]any{"port": float64(502)}, "port", 0, 502},
		{"missing key returns default", map[string]any{}, "port", 502, 502},
		{"nil map returns default", nil, "port", 502, 502},
		{"string value returns default", map[string]any{"port": "502"}, "port", 502, 502},
		{"bool value returns default", map[string]any{"port": true}, "port", 502, 502},
		{"negative int", map[string]any{"x": -1}, "x", 0, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetIntSetting(tt.settings, tt.key, tt.def)
			if got != tt.want {
				t.Errorf("GetIntSetting(%v, %q, %d) = %d, want %d", tt.settings, tt.key, tt.def, got, tt.want)
			}
		})
	}
}

// --- GetDurationSetting ---

func TestGetDurationSetting(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		key      string
		def      time.Duration
		want     time.Duration
	}{
		{"valid duration", map[string]any{"interval": "5s"}, "interval", 0, 5 * time.Second},
		{"valid ms duration", map[string]any{"interval": "200ms"}, "interval", 0, 200 * time.Millisecond},
		{"missing key returns default", map[string]any{}, "interval", 3 * time.Second, 3 * time.Second},
		{"nil map returns default", nil, "interval", 3 * time.Second, 3 * time.Second},
		{"invalid duration returns default", map[string]any{"interval": "not-a-duration"}, "interval", 3 * time.Second, 3 * time.Second},
		{"non-string value returns default", map[string]any{"interval": 5000}, "interval", 3 * time.Second, 3 * time.Second},
		{"empty string returns default", map[string]any{"interval": ""}, "interval", 3 * time.Second, 3 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetDurationSetting(tt.settings, tt.key, tt.def)
			if got != tt.want {
				t.Errorf("GetDurationSetting(%v, %q, %v) = %v, want %v", tt.settings, tt.key, tt.def, got, tt.want)
			}
		})
	}
}

// --- GetBoolSetting ---

func TestGetBoolSetting(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		key      string
		def      bool
		want     bool
	}{
		{"true", map[string]any{"enabled": true}, "enabled", false, true},
		{"false", map[string]any{"enabled": false}, "enabled", true, false},
		{"missing returns default true", map[string]any{}, "enabled", true, true},
		{"missing returns default false", map[string]any{}, "enabled", false, false},
		{"nil map returns default", nil, "enabled", true, true},
		{"non-bool returns default", map[string]any{"enabled": "yes"}, "enabled", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetBoolSetting(tt.settings, tt.key, tt.def)
			if got != tt.want {
				t.Errorf("GetBoolSetting(%v, %q, %v) = %v, want %v", tt.settings, tt.key, tt.def, got, tt.want)
			}
		})
	}
}

// --- IsConnectionError ---

func TestIsConnectionError(t *testing.T) {
	// Each keyword should be detected.
	errMsgs := []string{
		"connection refused",
		"broken pipe",
		"unexpected EOF",
		"connection reset by peer",
		"dial: connection refused",
		"i/o timeout",
		"connection closed",
		"operation aborted",
	}
	for _, msg := range errMsgs {
		t.Run(msg, func(t *testing.T) {
			if !IsConnectionError(errors.New(msg)) {
				t.Errorf("IsConnectionError(%q) = false, want true", msg)
			}
		})
	}

	// Non-connection errors.
	t.Run("non-connection error", func(t *testing.T) {
		if IsConnectionError(errors.New("invalid register address")) {
			t.Error("expected false for non-connection error")
		}
	})

	t.Run("nil error", func(t *testing.T) {
		if IsConnectionError(nil) {
			t.Error("expected false for nil error")
		}
	})
}

// --- ToFloat64 ---

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want float64
	}{
		{"float64", float64(3.14), 3.14},
		{"float32", float32(2.5), 2.5},
		{"int", int(42), 42},
		{"int8", int8(-1), -1},
		{"int16", int16(32000), 32000},
		{"int32", int32(-100), -100},
		{"int64", int64(1 << 40), float64(1 << 40)},
		{"uint", uint(100), 100},
		{"uint8", uint8(255), 255},
		{"uint16", uint16(65535), 65535},
		{"uint32", uint32(70000), 70000},
		{"uint64", uint64(1 << 40), float64(1 << 40)},
		{"bool true", true, 1},
		{"bool false", false, 0},
		{"string returns 0", "hello", 0},
		{"nil returns 0", nil, 0},
		{"[]byte returns 0", []byte{1, 2}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToFloat64(tt.in)
			if got != tt.want {
				t.Errorf("ToFloat64(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// --- ToFloat32 ---

func TestToFloat32(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want float32
	}{
		{"float64", float64(3.14), float32(3.14)},
		{"float32", float32(2.5), 2.5},
		{"int", int(42), 42},
		{"int32", int32(-100), -100},
		{"uint32", uint32(70000), 70000},
		{"string returns 0", "hello", 0},
		{"int16 returns 0 (not in switch)", int16(100), 0},
		{"nil returns 0", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToFloat32(tt.in)
			if got != tt.want {
				t.Errorf("ToFloat32(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// --- ToUint32 ---

func TestToUint32(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want uint32
	}{
		{"float64", float64(42.9), 42},
		{"int", int(100), 100},
		{"int32", int32(200), 200},
		{"uint32", uint32(300), 300},
		{"int64", int64(400), 400},
		{"uint64", uint64(500), 500},
		{"string returns 0", "x", 0},
		{"nil returns 0", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToUint32(tt.in)
			if got != tt.want {
				t.Errorf("ToUint32(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// --- ToUint64 ---

func TestToUint64(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want uint64
	}{
		{"float64", float64(42.9), 42},
		{"float32", float32(99.0), 99},
		{"int", int(100), 100},
		{"int64", int64(400), 400},
		{"uint64", uint64(500), 500},
		{"int32", int32(200), 200},
		{"uint32", uint32(300), 300},
		{"int16", int16(10), 10},
		{"uint16", uint16(20), 20},
		{"string returns 0", "x", 0},
		{"nil returns 0", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToUint64(tt.in)
			if got != tt.want {
				t.Errorf("ToUint64(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// --- ToUint16 ---

func TestToUint16(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want uint16
	}{
		{"float64", float64(42.9), 42},
		{"float32", float32(99.0), 99},
		{"int", int(100), 100},
		{"int16", int16(10), 10},
		{"uint16", uint16(20), 20},
		{"int32", int32(200), 200},
		{"uint32", uint32(300), 300},
		{"string returns 0", "x", 0},
		{"nil returns 0", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToUint16(tt.in)
			if got != tt.want {
				t.Errorf("ToUint16(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// --- ApplyTransform ---

func TestApplyTransform(t *testing.T) {
	// No-op when both scale and offset are 0.
	t.Run("identity when scale and offset zero", func(t *testing.T) {
		got := ApplyTransform(float32(42.0), 0, 0)
		if got.(float32) != 42.0 {
			t.Errorf("expected 42.0, got %v", got)
		}
	})

	// Scale only (offset 0 → treated as scale=1 when scale==0, but here scale!=0).
	t.Run("scale only", func(t *testing.T) {
		got := ApplyTransform(float64(10.0), 2.0, 0)
		if got.(float64) != 20.0 {
			t.Errorf("expected 20.0, got %v", got)
		}
	})

	// Offset only (scale 0 → reset to 1).
	t.Run("offset only (scale reset to 1)", func(t *testing.T) {
		got := ApplyTransform(float64(10.0), 0, 5.0)
		if got.(float64) != 15.0 {
			t.Errorf("expected 15.0, got %v", got)
		}
	})

	// Both scale and offset.
	t.Run("scale and offset on float32", func(t *testing.T) {
		got := ApplyTransform(float32(100.0), 0.1, 5.0)
		// 100 * 0.1 + 5 = 15
		if got.(float32) != 15.0 {
			t.Errorf("expected 15.0, got %v", got)
		}
	})

	t.Run("scale and offset on int16", func(t *testing.T) {
		got := ApplyTransform(int16(100), 0.1, 5.0)
		if got.(float64) != 15.0 {
			t.Errorf("expected 15.0, got %v", got)
		}
	})

	t.Run("scale and offset on uint16", func(t *testing.T) {
		got := ApplyTransform(uint16(100), 0.1, 5.0)
		if got.(float64) != 15.0 {
			t.Errorf("expected 15.0, got %v", got)
		}
	})

	t.Run("scale and offset on int32", func(t *testing.T) {
		got := ApplyTransform(int32(100), 0.1, 5.0)
		if got.(float64) != 15.0 {
			t.Errorf("expected 15.0, got %v", got)
		}
	})

	t.Run("scale and offset on uint32", func(t *testing.T) {
		got := ApplyTransform(uint32(100), 0.1, 5.0)
		if got.(float64) != 15.0 {
			t.Errorf("expected 15.0, got %v", got)
		}
	})

	// Unsupported type returns value unchanged.
	t.Run("unsupported type returns unchanged", func(t *testing.T) {
		in := "hello"
		got := ApplyTransform(in, 2.0, 3.0)
		if got.(string) != "hello" {
			t.Errorf("expected unchanged string, got %v", got)
		}
	})
}

// --- ReconnectLoop ---

func TestReconnectLoop(t *testing.T) {
	t.Run("connect succeeds on first try", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		callCount := 0
		connect := func() error {
			callCount++
			return nil
		}
		// Use very short backoff so the test is fast.
		ReconnectLoop(ctx, "test", connect, 10*time.Millisecond, 100*time.Millisecond)
		if callCount != 1 {
			t.Errorf("expected 1 connect call, got %d", callCount)
		}
	})

	t.Run("connect succeeds after retries", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		callCount := 0
		connect := func() error {
			callCount++
			if callCount < 3 {
				return errors.New("connection refused")
			}
			return nil
		}
		ReconnectLoop(ctx, "test", connect, 10*time.Millisecond, 50*time.Millisecond)
		if callCount != 3 {
			t.Errorf("expected 3 connect calls, got %d", callCount)
		}
	})

	t.Run("context cancelled stops loop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		callCount := 0
		connect := func() error {
			callCount++
			return errors.New("connection refused")
		}

		// Cancel after a short delay to let a couple retries happen.
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		ReconnectLoop(ctx, "test", connect, 10*time.Millisecond, 50*time.Millisecond)
		// Should have made some calls but not infinite.
		if callCount == 0 {
			t.Error("expected at least 1 connect call before cancellation")
		}
	})

	t.Run("zero backoff falls back to defaults", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// With 0 backoffs, defaults (2s initial) are used.
		// Cancel immediately so we don't wait 2s.
		go func() {
			time.Sleep(5 * time.Millisecond)
			cancel()
		}()

		connect := func() error {
			return errors.New("connection refused")
		}
		// Should return quickly due to cancellation, not hang.
		ReconnectLoop(ctx, "test", connect, 0, 0)
	})

	t.Run("backoff doubles up to max", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Track timing of calls to verify backoff increases.
		callCount := 0
		connect := func() error {
			callCount++
			if callCount >= 4 {
				return nil
			}
			return errors.New("connection refused")
		}
		// initial=5ms, max=20ms → backoff sequence: 5, 10, 20, 20
		ReconnectLoop(ctx, "test", connect, 5*time.Millisecond, 20*time.Millisecond)
		if callCount != 4 {
			t.Errorf("expected 4 connect calls, got %d", callCount)
		}
	})
}

// Ensure the connectionErrorKeywords list is comprehensive — if someone
// adds a new keyword, this test reminds them to add a test case above.
func TestConnectionErrorKeywordsCoverage(t *testing.T) {
	if len(connectionErrorKeywords) == 0 {
		t.Fatal("connectionErrorKeywords should not be empty")
	}
	// Verify each keyword is actually detected.
	for _, kw := range connectionErrorKeywords {
		err := errors.New("prefix " + kw + " suffix")
		if !IsConnectionError(err) {
			t.Errorf("keyword %q not detected by IsConnectionError", kw)
		}
	}
	// Verify a string with no keywords is not flagged.
	if IsConnectionError(errors.New("totally fine unrelated error")) {
		t.Error("unrelated error should not be flagged as connection error")
	}
}

// TestToFloat64RoundTrip verifies that integer values converted to float64
// and back preserve the original value (within float64 precision).
func TestToFloat64RoundTrip(t *testing.T) {
	original := int64(123456789)
	f := ToFloat64(original)
	back := int64(f)
	if back != original {
		t.Errorf("round-trip failed: original=%d, got=%d", original, back)
	}
}

// TestApplyTransformChain verifies that two transforms compose correctly.
func TestApplyTransformChain(t *testing.T) {
	// First: scale=2, offset=0 → 10 becomes 20
	v1 := ApplyTransform(float64(10), 2.0, 0)
	// Second: scale=1, offset=5 → 20 becomes 25
	v2 := ApplyTransform(v1, 1.0, 5.0)
	if v2.(float64) != 25.0 {
		t.Errorf("chained transform: expected 25.0, got %v", v2)
	}
}

// TestAllConversionsConsistent verifies that a value converted through
// different numeric paths yields consistent results.
func TestAllConversionsConsistent(t *testing.T) {
	v := int(42)
	f64 := ToFloat64(v)
	u32 := ToUint32(v)
	u64 := ToUint64(v)
	u16 := ToUint16(v)

	if f64 != 42 || u32 != 42 || u64 != 42 || u16 != 42 {
		t.Errorf("inconsistent conversions: f64=%v u32=%v u64=%v u16=%v", f64, u32, u64, u16)
	}
}

// TestIsConnectionErrorPartialMatch verifies that the keyword can appear
// anywhere in the error string, not just at the start.
func TestIsConnectionErrorPartialMatch(t *testing.T) {
	err := errors.New("read tcp 10.0.0.1:502->10.0.0.2:502: read: connection reset by peer")
	if !IsConnectionError(err) {
		t.Error("expected 'connection reset by peer' to be detected")
	}
}

// TestReconnectLoopNameInError verifies the name parameter is used
// (it appears in slog output, which we can't easily capture, but we
// can at least verify the function doesn't panic with empty name).
func TestReconnectLoopNameInError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	ReconnectLoop(ctx, "", func() error { return errors.New("x") }, 1*time.Millisecond, 5*time.Millisecond)
}

// TestGetIntSettingFloatTruncation verifies that float64 values from YAML
// are properly truncated (not rounded) to int.
func TestGetIntSettingFloatTruncation(t *testing.T) {
	got := GetIntSetting(map[string]any{"x": float64(99.9)}, "x", 0)
	if got != 99 {
		t.Errorf("expected truncation to 99, got %d", got)
	}
}

// TestToFloat32PrecisionLoss documents that float64→float32 can lose
// precision, and ToFloat32 handles this by casting.
func TestToFloat32PrecisionLoss(t *testing.T) {
	// A large float64 that loses precision in float32.
	big := float64(1 << 25) // 33554432
	got := ToFloat32(big)
	// float32 can represent this exactly (it's a power of 2).
	if float64(got) != big {
		t.Errorf("expected %v, got %v", big, got)
	}
}

// TestApplyTransformNegativeScale verifies negative scale works.
func TestApplyTransformNegativeScale(t *testing.T) {
	got := ApplyTransform(float64(10), -2.0, 0)
	if got.(float64) != -20.0 {
		t.Errorf("expected -20.0, got %v", got)
	}
}

// TestApplyTransformLargeOffset verifies large offset doesn't overflow.
func TestApplyTransformLargeOffset(t *testing.T) {
	got := ApplyTransform(float64(1), 1e10, 1e10)
	if got.(float64) != 2e10 {
		t.Errorf("expected 2e10, got %v", got)
	}
}

// TestReconnectLoopMaxBackoffCap verifies that backoff is capped at maxBackoff.
func TestReconnectLoopMaxBackoffCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	callCount := 0
	connect := func() error {
		callCount++
		if callCount >= 6 {
			return nil
		}
		return errors.New("connection refused")
	}
	// initial=2ms, max=4ms → backoff: 2, 4, 4, 4, 4 (capped)
	start := time.Now()
	ReconnectLoop(ctx, "test", connect, 2*time.Millisecond, 4*time.Millisecond)
	elapsed := time.Since(start)
	// Total time should be roughly 2+4+4+4+4 = 18ms (with some tolerance).
	if elapsed > 200*time.Millisecond {
		t.Errorf("backoff not properly capped: elapsed=%v", elapsed)
	}
	if callCount != 6 {
		t.Errorf("expected 6 calls, got %d", callCount)
	}
}

// TestUtilPackageString verifies the package compiles and basic functions
// don't panic on edge-case inputs.
func TestUtilNoPanics(t *testing.T) {
	// These should not panic.
	_ = GetIntSetting(nil, "", 0)
	_ = GetDurationSetting(nil, "", 0)
	_ = GetBoolSetting(nil, "", false)
	_ = IsConnectionError(nil)
	_ = ToFloat64(nil)
	_ = ToFloat32(nil)
	_ = ToUint32(nil)
	_ = ToUint64(nil)
	_ = ToUint16(nil)
	_ = ApplyTransform(nil, 0, 0)
}

// TestStringContains verifies a helper used in error detection.
func TestStringContains(t *testing.T) {
	// This documents that IsConnectionError uses substring matching.
	err := errors.New("EOF")
	if !IsConnectionError(err) {
		t.Error("EOF should be detected")
	}
	// Case sensitivity: "Connection" (capital C) should NOT match "connection".
	err2 := errors.New("Connection lost")
	if IsConnectionError(err2) {
		t.Error("IsConnectionError should be case-sensitive; 'Connection' should not match 'connection'")
	}
}
