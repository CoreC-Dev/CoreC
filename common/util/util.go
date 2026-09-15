// Package util provides shared helper functions used across drivers,
// transports, and the rule engine.
package util

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"
)

// GetIntSetting extracts an int from a settings map, falling back to
// defaultVal if the key is missing or the value is not an int/float64.
// YAML unmarshalling produces float64 for unquoted numbers, so both
// int and float64 are accepted.
func GetIntSetting(settings map[string]any, key string, defaultVal int) int {
	if v, ok := settings[key].(int); ok {
		return v
	}
	if v, ok := settings[key].(float64); ok {
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

// connectionErrorKeywords are substrings that indicate a lost connection.
var connectionErrorKeywords = []string{
	"connection", "broken pipe", "EOF", "reset", "refused", "timeout", "closed", "aborted",
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

// ToFloat64 converts any numeric value (including bool) to float64.
// Non-numeric values return 0. bool is treated as 1 (true) / 0 (false).
func ToFloat64(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int8:
		return float64(val)
	case int16:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case uint:
		return float64(val)
	case uint8:
		return float64(val)
	case uint16:
		return float64(val)
	case uint32:
		return float64(val)
	case uint64:
		return float64(val)
	case bool:
		if val {
			return 1
		}
		return 0
	default:
		return 0
	}
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
	case int16:
		return float64(v)*scale + offset
	case uint16:
		return float64(v)*scale + offset
	case int32:
		return float64(v)*scale + offset
	case uint32:
		return float64(v)*scale + offset
	default:
		return value
	}
}

// jitteredBackoff applies ±20% random jitter to a backoff duration.
//
// When the network recovers after an outage, every driver that lost its
// connection around the same time would otherwise reconnect
// simultaneously (the "thundering herd" problem), overwhelming the
// recovered broker/PLC with a burst of connection attempts. Jitter
// spreads these attempts over a wider window so they reconnect in a
// staggered fashion.
//
// The returned duration is in the range [0.8*d, 1.2*d]. A non-positive
// input is returned unchanged so the helper is safe to apply to
// defaulted/zero backoffs without surprising behaviour.
func jitteredBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	// d * (0.8 + rand(0.4)) gives ±20% randomness in [0.8, 1.2].
	return time.Duration(float64(d) * (0.8 + rand.Float64()*0.4))
}

// ReconnectLoopWithBreaker repeatedly calls connect with exponential
// backoff until it succeeds or ctx is cancelled. The backoff starts at
// initialBackoff and doubles on each failure, capped at maxBackoff.
// <=0 values fall back to 2s initial and 30s max.
//
// A ±20% random jitter is applied to every wait so that when the network
// recovers, drivers that disconnected simultaneously do not all reconnect
// at once (thundering herd). The base backoff progression (exponential
// doubling and the circuit-breaker interval) is preserved; only the
// actual sleep duration is jittered.
//
// After maxFailures consecutive failures (maxFailures > 0), a circuit
// breaker trips and the backoff is increased to circuitBreakerBackoff
// (5 minutes) to avoid hammering a permanently offline device. The
// breaker resets on the next successful connection. maxFailures <= 0
// disables the breaker (pure exponential backoff).
func ReconnectLoopWithBreaker(ctx context.Context, name string, connect func() error, initialBackoff, maxBackoff time.Duration, maxFailures int) {
	backoff := initialBackoff
	if backoff <= 0 {
		backoff = 2 * time.Second
	}
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}

	// Circuit breaker state.
	const circuitBreakerBackoff = 5 * time.Minute
	failures := 0
	breakerTripped := false

	for {
		// Apply ±20% jitter to the base backoff for this iteration.
		// The base value is kept unjittered so the exponential
		// progression and circuit-breaker interval remain stable.
		wait := jitteredBackoff(backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		if err := connect(); err != nil {
			failures++
			slog.Warn("reconnect failed",
				"name", name, "error", err, "failures", failures,
				"backoff", backoff, "waited", wait)

			// Circuit breaker: after maxFailures consecutive failures,
			// switch to a long fixed interval to stop hammering the
			// device and reduce resource consumption.
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
