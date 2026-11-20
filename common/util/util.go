// Package util provides shared helper functions used across drivers,
// transports, and the rule engine.
package util

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Fallbacks for non-positive backoff args; mirrored with
// core.DefaultReconnectBackoff/DefaultMaxReconnectBackoff (util stays a
// leaf package).
const (
	defaultBackoffFallback       = 2 * time.Second
	defaultMaxBackoffFallback    = 30 * time.Second
	defaultCircuitBreakerBackoff = 5 * time.Minute
)

// GetIntSetting extracts an int from a settings map, falling back to
// defaultVal if the key is missing or the value is not numeric.
// YAML unmarshalling may produce int, uint64, int64, or float64
// depending on the parser and value magnitude, so all numeric kinds
// are accepted.
func GetIntSetting(settings map[string]any, key string, defaultVal int) int {
	switch v := settings[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	}
	return defaultVal
}

// GetDurationSetting extracts a duration string (e.g. "5s") from a
// settings map, falling back to defaultVal if missing or unparseable.
func GetDurationSetting(settings map[string]any, key string, defaultVal time.Duration) time.Duration {
	if v, ok := settings[key].(string); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}

// GetBoolSetting extracts a bool from a settings map, falling back to
// defaultVal if the key is missing or the value is not a bool.
func GetBoolSetting(settings map[string]any, key string, defaultVal bool) bool {
	if v, ok := settings[key].(bool); ok {
		return v
	}
	return defaultVal
}

// GetStringSetting extracts a string from a settings map, falling back to
// defaultVal if the key is missing or the value is not a string. YAML
// unmarshalling produces a string for quoted scalar values, so only the
// string kind is accepted; other types fall back to defaultVal.
func GetStringSetting(settings map[string]any, key, defaultVal string) string {
	if v, ok := settings[key].(string); ok {
		return v
	}
	return defaultVal
}

// connectionErrorKeywords are substrings that indicate a lost connection.
var connectionErrorKeywords = []string{
	"connection", "broken pipe", "EOF", "reset", "refused", "timeout", "closed", "aborted",
	// "deadline exceeded" is Go's context.DeadlineExceeded message — the
	// read-deadline timeout of a wedged TCP connection.
	"deadline exceeded",
}

// IsConnectionError reports whether err looks like a connection-lost
// error by checking for common keywords in its message.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	for _, kw := range connectionErrorKeywords {
		if strings.Contains(errStr, kw) {
			return true
		}
	}
	return false
}

// ToFloat64OK converts any numeric value to float64 and returns true.
// Non-numeric values (bool, string, nil, etc.) return 0 and false.
// Unlike ToFloat64, bool is NOT treated as numeric — callers that need
// to reject non-numeric values (e.g. transform expressions) should use this.
func ToFloat64OK(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int8:
		return float64(val), true
	case int16:
		return float64(val), true
	case int32:
		return float64(val), true
	case int64:
		return float64(val), true
	case uint:
		return float64(val), true
	case uint8:
		return float64(val), true
	case uint16:
		return float64(val), true
	case uint32:
		return float64(val), true
	case uint64:
		return float64(val), true
	}
	return 0, false
}

// ToFloat64 converts any numeric value (including bool) to float64.
// Non-numeric values return 0. bool is treated as 1 (true) / 0 (false).
func ToFloat64(v any) float64 {
	f, ok := ToFloat64OK(v)
	if ok {
		return f
	}
	if val, isBool := v.(bool); isBool {
		if val {
			return 1
		}
		return 0
	}
	return 0
}

// ToFloat32 converts any numeric value to float32. Non-numeric returns 0.
func ToFloat32(v any) float32 {
	switch val := v.(type) {
	case float64:
		return float32(val)
	case float32:
		return val
	case int:
		return float32(val)
	case int32:
		return float32(val)
	case uint32:
		return float32(val)
	default:
		return 0
	}
}

// ToUint32 converts any numeric value to uint32. Non-numeric returns 0.
func ToUint32(v any) uint32 {
	switch val := v.(type) {
	case float64:
		return uint32(val)
	case int:
		return uint32(val)
	case int32:
		return uint32(val)
	case uint32:
		return val
	case int64:
		return uint32(val)
	case uint64:
		return uint32(val)
	default:
		return 0
	}
}

// ToUint64 converts any numeric value to uint64. Non-numeric returns 0.
func ToUint64(v any) uint64 {
	switch val := v.(type) {
	case float64:
		return uint64(val)
	case float32:
		return uint64(val)
	case int:
		return uint64(val)
	case int64:
		return uint64(val)
	case uint64:
		return val
	case int32:
		return uint64(val)
	case uint32:
		return uint64(val)
	case int16:
		return uint64(val)
	case uint16:
		return uint64(val)
	default:
		return 0
	}
}

// ToUint16 converts a numeric any value to uint16.
func ToUint16(v any) uint16 {
	switch val := v.(type) {
	case float64:
		return uint16(val)
	case float32:
		return uint16(val)
	case int:
		return uint16(val)
	case int16:
		return uint16(val)
	case uint16:
		return val
	case int32:
		return uint16(val)
	case uint32:
		return uint16(val)
	default:
		return 0
	}
}

// ApplyTransform applies scale and offset to a numeric value.
// If scale and offset are both 0, the value is returned unchanged.
//
// All integer widths and both float widths are covered (int8…int64,
// uint8…uint64, float32/float64); scaled integers become float64.
func ApplyTransform(value any, scale, offset float64) any {
	if scale == 0 && offset == 0 {
		return value
	}
	if scale == 0 {
		scale = 1
	}

	switch v := value.(type) {
	case float32:
		return float32(float64(v)*scale + offset)
	case float64:
		return v*scale + offset
	case int8:
		return float64(v)*scale + offset
	case uint8:
		return float64(v)*scale + offset
	case int16:
		return float64(v)*scale + offset
	case uint16:
		return float64(v)*scale + offset
	case int32:
		return float64(v)*scale + offset
	case uint32:
		return float64(v)*scale + offset
	case int64:
		return float64(v)*scale + offset
	case uint64:
		return float64(v)*scale + offset
	case int:
		return float64(v)*scale + offset
	default:
		return value
	}
}

// InvertTransform inverts the read-side linear transform applied by
// ApplyTransform so a write carrying the engineering value is converted
// back to the raw device representation before the driver encodes it:
// raw = (value - offset) / scale.
//
// The zero-value normalization mirrors ApplyTransform exactly so the two
// stay inverse under every configuration spelling:
//   - scale==0 && offset==0 means "no transform configured" — pass through.
//   - scale==0 with offset!=0 reads as value+offset (ApplyTransform
//     normalizes the zero scale to 1), so the inverse is value-offset.
//
// Non-numeric values (bool, string, bytes) are never scaled on the read
// path and pass through unchanged here as well, keeping reads and writes
// symmetric per tag type.
func InvertTransform(value any, scale, offset float64) any {
	if scale == 0 && offset == 0 {
		return value
	}
	if scale == 0 {
		scale = 1
	}

	switch v := value.(type) {
	case float32:
		return (float64(v) - offset) / scale
	case float64:
		return (v - offset) / scale
	case int8:
		return (float64(v) - offset) / scale
	case uint8:
		return (float64(v) - offset) / scale
	case int16:
		return (float64(v) - offset) / scale
	case uint16:
		return (float64(v) - offset) / scale
	case int32:
		return (float64(v) - offset) / scale
	case uint32:
		return (float64(v) - offset) / scale
	case int64:
		return (float64(v) - offset) / scale
	case uint64:
		return (float64(v) - offset) / scale
	case int:
		return (float64(v) - offset) / scale
	default:
		return value
	}
}

// jitteredBackoff applies ±20% random jitter to a backoff duration.
// The returned duration is in the range [0.8*d, 1.2*d]; a non-positive
// input is returned unchanged.
func jitteredBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return time.Duration(float64(d) * (0.8 + rand.Float64()*0.4))
}

// ReconnectOpts bundles the parameters for reconnect loop configuration.
type ReconnectOpts struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxFailures    int
}

// ReconnectLoopOpts is the opts-struct variant of ReconnectLoopWithBreakerCounted.
func ReconnectLoopOpts(ctx context.Context, name string, connect func() error, opts ReconnectOpts, counter *atomic.Uint64) {
	ReconnectLoopWithBreakerCounted(ctx, name, connect, opts.InitialBackoff, opts.MaxBackoff, opts.MaxFailures, counter)
}

// ReconnectLoopWithBreaker behaves like ReconnectLoopWithBreakerCounted
// but does not track reconnect attempts. Use the counted variant when a
// driver wants to expose a reconnect Prometheus metric.
//
// Backoff starts at initialBackoff and doubles after each failure, capped at
// maxBackoff. A circuit-breaker trips after maxFailures consecutive failures
// (maxFailures > 0) and increases the backoff to 5 minutes. The breaker
// resets on the next successful connection. maxFailures <= 0 disables the
// breaker (pure exponential backoff).
func ReconnectLoopWithBreaker(ctx context.Context, name string, connect func() error, initialBackoff, maxBackoff time.Duration, maxFailures int) {
	ReconnectLoopWithBreakerCounted(ctx, name, connect, initialBackoff, maxBackoff, maxFailures, nil)
}

// ReconnectLoopWithBreakerCounted behaves like ReconnectLoopWithBreaker
// and additionally increments counter (when non-nil) at the start of each
// reconnect attempt — whether that attempt ultimately succeeds or fails.
// Drivers pass an atomic.Uint64 held on the driver struct so the value
// can be surfaced via Status() and exposed as a Prometheus counter.
//
// The counter is incremented before connect() is invoked, so even if the
// caller cancels the context mid-attempt, every attempt that began is
// reflected in the count. A nil counter disables counting and makes this
// equivalent to ReconnectLoopWithBreaker.
func ReconnectLoopWithBreakerCounted(ctx context.Context, name string, connect func() error, initialBackoff, maxBackoff time.Duration, maxFailures int, counter *atomic.Uint64) {
	backoff := initialBackoff
	if backoff <= 0 {
		backoff = defaultBackoffFallback
	}
	if maxBackoff <= 0 {
		maxBackoff = defaultMaxBackoffFallback
	}

	// Circuit breaker state.
	const circuitBreakerBackoff = defaultCircuitBreakerBackoff
	failures := 0
	breakerTripped := false

	for {
		// Only the wait is jittered; the base backoff progression stays
		// unjittered.
		wait := jitteredBackoff(backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		if counter != nil {
			counter.Add(1)
		}

		if err := connect(); err != nil {
			failures++
			slog.Warn("reconnect failed",
				"name", name, "error", err, "failures", failures,
				"backoff", backoff, "waited", wait)

			// Circuit breaker: after maxFailures consecutive failures,
			// switch to a long fixed interval.
			if maxFailures > 0 && failures >= maxFailures && !breakerTripped {
				breakerTripped = true
				backoff = circuitBreakerBackoff
				slog.Warn("reconnect circuit breaker tripped, reducing frequency",
					"name", name, "failures", failures, "retry_interval", backoff)
			} else if !breakerTripped {
				backoff = min(backoff*2, maxBackoff)
			}
			continue
		}

		if breakerTripped {
			slog.Info("reconnected after circuit breaker reset", "name", name, "failures", failures)
		} else {
			slog.Info("reconnected", "name", name)
		}
		return
	}
}

// RejectUnknownSettings fails with an error listing the valid keys when
// a settings map contains keys outside the known set. known must list
// every key the component's Init parses; the per-package drift tests
// enforce that.
func RejectUnknownSettings(settings map[string]any, known map[string]bool, kind string) error {
	if len(settings) == 0 {
		return nil
	}
	var unknown []string
	for k := range settings {
		if known[k] {
			continue
		}
		unknown = append(unknown, k)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	valid := make([]string, 0, len(known))
	for k := range known {
		valid = append(valid, k)
	}
	sort.Strings(valid)
	return fmt.Errorf("unknown %s setting(s): %s (valid: %s)", kind,
		strings.Join(unknown, ", "), strings.Join(valid, ", "))
}
