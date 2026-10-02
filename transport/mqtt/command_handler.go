package mqtt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// handleCommandMessage processes a single command-topic message: it
// verifies the HMAC-SHA256 signature (when a command-secret is
// configured), unmarshals the payload into a WriteCommand, and forwards
// it to the command channel.  Messages that fail authentication or
// parsing are logged and dropped.
//
// Replay protection: when a command-secret is configured and the message
// includes a "timestamp" field (Unix milliseconds), the signature is
// verified over the canonical signing bytes (payload with the signature
// fields removed) and the timestamp must fall within ±commandMaxSkew of
// the current time.  Messages without a timestamp are accepted for
// backward compatibility when command-strict-replay is false (a warning
// is logged, since such messages cannot be protected against replay),
// and rejected when command-strict-replay is true.
func (t *MQTTTransport) handleCommandMessage(topic string, payload []byte) {
	slog.Debug("mqtt command received",
		"topic", topic,
		"payload_size", len(payload),
	)

	// Message-level authentication: when a command-secret is configured,
	// every command message must carry a valid HMAC-SHA256 signature.
	// The signature is carried in either an "X-Signature" or "signature"
	// JSON field.  This prevents any client that can merely publish to
	// the command topic from injecting arbitrary control commands.
	if t.commandSecret != "" {
		provided := extractCommandSignature(payload)
		ts, hasTS := extractCommandTimestamp(payload)

		if !hasTS {
			// Legacy path: no timestamp.  Verify the HMAC over the
			// canonical signing bytes (signature fields excluded) so
			// that properly-constructed signed commands are accepted.
			// This preserves backward compatibility with existing
			// signed-command senders while making the signature
			// verifiable (the signature value is not part of the
			// bytes being signed).
			if !verifyHMACSignature(t.commandSecret, commandSigningBytes(payload), provided) {
				slog.Error("mqtt: command signature verification failed, dropping command",
					"topic", topic, "name", t.name)
				return
			}
			if t.commandStrictReplay {
				slog.Error("mqtt: command rejected, strict replay protection requires a timestamp",
					"topic", topic, "name", t.name)
				return
			}
			slog.Warn("mqtt: command accepted without timestamp; replay protection not enforced",
				"topic", topic, "name", t.name)
		} else {
			// Replay-protected path: verify the HMAC over the canonical
			// signing bytes (signature excluded, timestamp included so
			// the timestamp is authenticated and cannot be forged) and
			// enforce timestamp freshness within ±commandMaxSkew.
			if !verifyHMACSignature(t.commandSecret, commandSigningBytes(payload), provided) {
				slog.Error("mqtt: command signature verification failed, dropping command",
					"topic", topic, "name", t.name)
				return
			}
			if !t.isCommandTimestampFresh(ts) {
				slog.Error("mqtt: command timestamp outside allowed skew, rejected as replay",
					"topic", topic, "name", t.name,
					"skew", t.commandMaxSkew, "timestamp", ts)
				return
			}
			// True replay detection: compute a hash of the authenticated
			// payload and check it against the seen-commands cache. This
			// prevents the same message from being accepted twice within
			// the skew window, even if an attacker re-sends it.
			if t.replayCache != nil {
				msgHash := fmt.Sprintf("%x", sha256.Sum256(payload))
				if !t.replayCache.checkAndAdd(msgHash) {
					slog.Error("mqtt: command rejected as duplicate (replay detected)",
						"topic", topic, "name", t.name)
					return
				}
			}
		}
	}

	var cmd core.WriteCommand
	if err := json.Unmarshal(payload, &cmd); err != nil {
		slog.Error("mqtt: failed to parse command",
			"topic", topic,
			"error", err,
		)
		return
	}

	select {
	case t.commandCh <- cmd:
	default:
		// commandCh is full. The paho message callback MUST stay
		// non-blocking (Order=true dispatches callbacks serially on a
		// single goroutine; blocking would stall the inbound pipeline and
		// risk a broker disconnect via keepalive timeout). So instead of
		// blocking for backpressure, we record the command in a
		// transport-local bounded dead-letter store for operator
		// inspection and bump a dropped-commands counter.
		t.droppedCommands.Add(1)
		t.addCommandDeadLetter(core.DeadLetterEntry{
			Command:  cmd,
			Error:    "command channel full at ingress",
			FailedAt: time.Now(),
		})
		slog.Warn("mqtt command channel full, command diverted to dead-letter store",
			"driver", cmd.Driver, "tag", cmd.Tag)
	}
}

// addCommandDeadLetter appends a dead-letter entry for a command that could
// not be enqueued, evicting the oldest entry when the store is full. It is
// mutex-guarded and never blocks (bounded slice, no I/O).
func (t *MQTTTransport) addCommandDeadLetter(entry core.DeadLetterEntry) {
	t.commandDeadLetterMu.Lock()
	defer t.commandDeadLetterMu.Unlock()
	t.commandDeadLetter = append(t.commandDeadLetter, entry)
	if len(t.commandDeadLetter) > t.commandDeadLetterMax {
		t.commandDeadLetter = t.commandDeadLetter[len(t.commandDeadLetter)-t.commandDeadLetterMax:]
	}
}

// CommandDeadLetterEntries returns a copy of the transport-local dead-letter
// entries for commands dropped at ingress (command channel full). It is
// intended for operator inspection / replay tooling and is not part of the
// core.Transport interface (callers type-assert to *MQTTTransport).
func (t *MQTTTransport) CommandDeadLetterEntries() []core.DeadLetterEntry {
	t.commandDeadLetterMu.Lock()
	defer t.commandDeadLetterMu.Unlock()
	cp := make([]core.DeadLetterEntry, len(t.commandDeadLetter))
	copy(cp, t.commandDeadLetter)
	return cp
}

// extractCommandSignature pulls the signature from a command JSON
// payload.  It accepts either an "X-Signature" field or a "signature"
// field (X-Signature takes precedence).  Returns an empty string when
// the payload is not valid JSON or neither field is present.
func extractCommandSignature(payload []byte) string {
	var sig struct {
		XSignature string `json:"X-Signature"`
		Signature  string `json:"signature"`
	}
	if err := json.Unmarshal(payload, &sig); err != nil {
		return ""
	}
	if sig.XSignature != "" {
		return sig.XSignature
	}
	return sig.Signature
}

// computeHMACSignature returns hex(HMAC-SHA256(secret, payload)).
func computeHMACSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyHMACSignature reports whether provided matches the expected
// HMAC-SHA256 signature of payload under secret, using a constant-time
// comparison.  An empty secret disables verification (always true) for
// backward compatibility.
func verifyHMACSignature(secret string, payload []byte, provided string) bool {
	if secret == "" {
		return true
	}
	expected := computeHMACSignature(secret, payload)
	return hmac.Equal([]byte(provided), []byte(expected))
}

// commandSigningBytes returns the canonical byte representation of a
// command payload with the signature fields removed, over which the
// command HMAC is computed.  Excluding the signature from the signed
// bytes makes the signature self-consistent and verifiable: the signature
// value is not part of the data being signed, so a sender can compute
// signature = HMAC(secret, commandSigningBytes(payload)) and embed it
// without a circular fixed-point.
//
// The payload is decoded into a map[string]json.RawMessage (preserving
// each field's exact bytes), the "X-Signature" and "signature" keys are
// removed, and the map is re-encoded.  Go's encoding/json emits map keys
// in sorted order, so the result is deterministic and identical to what a
// sender using the same canonicalisation produces — including the
// "timestamp" field, which is therefore authenticated and cannot be
// altered by an attacker without invalidating the signature.
//
// If the payload is not a valid JSON object, the raw bytes are returned
// unchanged (falling back to authenticating the full payload), preserving
// the historical behaviour for non-JSON command bodies.
func commandSigningBytes(payload []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return payload
	}
	delete(m, "X-Signature")
	delete(m, "signature")
	// Recursively canonicalize nested JSON values so that key ordering
	// is deterministic at every level, not just the top level. This
	// prevents signature verification failures when the sender and
	// receiver use different JSON libraries that produce different
	// key orders in nested objects.
	for k, v := range m {
		if canonical := canonicalRawJSON(v); canonical != nil {
			m[k] = canonical
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return b
}

// canonicalRawJSON recursively canonicalizes a JSON value by sorting
// object keys at every nesting level. Scalar values (numbers, strings,
// bools, null) are preserved as raw bytes to avoid precision loss from
// re-serialization (e.g. 1.0 → 1, 1e10 → 10000000000).
func canonicalRawJSON(raw json.RawMessage) json.RawMessage {
	// Try object: sort keys and recurse into values.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		for k, v := range obj {
			if canonical := canonicalRawJSON(v); canonical != nil {
				obj[k] = canonical
			}
		}
		if b, err := json.Marshal(obj); err == nil {
			return b
		}
		return raw
	}
	// Try array: recurse into elements (order is significant).
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		for i, v := range arr {
			if canonical := canonicalRawJSON(v); canonical != nil {
				arr[i] = canonical
			}
		}
		if b, err := json.Marshal(arr); err == nil {
			return b
		}
		return raw
	}
	// Scalar — return as-is to preserve exact representation.
	return raw
}

// extractCommandTimestamp pulls the "timestamp" field (Unix milliseconds)
// from a command JSON payload.  Returns ok=false when the payload is not
// valid JSON or the field is missing or not a number.  JSON numbers are
// decoded as float64; Unix millisecond timestamps (~1.7e12) are well
// within float64's exact-integer range (2^53), so no precision is lost.
func extractCommandTimestamp(payload []byte) (ts int64, ok bool) {
	var m struct {
		Timestamp any `json:"timestamp"`
	}
	if err := json.Unmarshal(payload, &m); err != nil {
		return 0, false
	}
	switch v := m.Timestamp.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	default:
		return 0, false
	}
}

// isCommandTimestampFresh reports whether the given Unix-millisecond
// timestamp falls within ±commandMaxSkew of the current time.  A skew of
// zero or less accepts any timestamp (freshness check disabled).
func (t *MQTTTransport) isCommandTimestampFresh(ts int64) bool {
	skew := t.commandMaxSkew.Milliseconds()
	if skew <= 0 {
		return true
	}
	now := time.Now().UnixMilli()
	diff := now - ts
	if diff < 0 {
		diff = -diff
	}
	return diff <= skew
}
