package mqtt

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// ─── Replay-protection config parsing ───────────────────────────────

func TestReplayConfigParsing(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		mtr := newInitdTransport(t, "replay-default", map[string]any{
			"broker":         "tcp://x:1883",
			"command-topic":  "cmd/+/set",
			"command-secret": "shh",
		})
		if mtr.commandMaxSkew != 5*time.Minute {
			t.Errorf("commandMaxSkew default: got %v, want 5m", mtr.commandMaxSkew)
		}
		if mtr.commandStrictReplay {
			t.Error("commandStrictReplay default should be false")
		}
	})

	t.Run("custom max-skew and strict", func(t *testing.T) {
		mtr := newInitdTransport(t, "replay-custom", map[string]any{
			"broker":                "tcp://x:1883",
			"command-topic":         "cmd/+/set",
			"command-secret":        "shh",
			"command-max-skew":      "90s",
			"command-strict-replay": true,
		})
		if mtr.commandMaxSkew != 90*time.Second {
			t.Errorf("commandMaxSkew: got %v, want 90s", mtr.commandMaxSkew)
		}
		if !mtr.commandStrictReplay {
			t.Error("commandStrictReplay should be true")
		}
	})
}

// ─── Canonical signing bytes ────────────────────────────────────────

func TestCommandSigningBytes(t *testing.T) {
	t.Run("removes signature fields", func(t *testing.T) {
		payload := []byte(`{"driver":"opc","tag":"t","value":1,"signature":"abc","X-Signature":"xyz"}`)
		got := commandSigningBytes(payload)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("result not valid JSON: %v", err)
		}
		if _, ok := m["signature"]; ok {
			t.Error("signature field should be removed")
		}
		if _, ok := m["X-Signature"]; ok {
			t.Error("X-Signature field should be removed")
		}
		if _, ok := m["driver"]; !ok {
			t.Error("driver field should be preserved")
		}
	})

	t.Run("preserves timestamp", func(t *testing.T) {
		payload := []byte(`{"driver":"opc","timestamp":1700000000000,"signature":"abc"}`)
		got := commandSigningBytes(payload)
		var m struct {
			Timestamp int64 `json:"timestamp"`
		}
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("result not valid JSON: %v", err)
		}
		if m.Timestamp != 1700000000000 {
			t.Errorf("timestamp not preserved: got %d", m.Timestamp)
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		payload := []byte(`{"z":"1","a":"2","signature":"s"}`)
		first := commandSigningBytes(payload)
		second := commandSigningBytes(payload)
		if !bytes.Equal(first, second) {
			t.Errorf("not deterministic:\n first: %s\nsecond: %s", first, second)
		}
		// Keys must be sorted (a before z), with no signature.
		if string(first) != `{"a":"2","z":"1"}` {
			t.Errorf("unexpected canonical form: %s", first)
		}
	})

	t.Run("non-JSON returns raw bytes", func(t *testing.T) {
		raw := []byte("not json")
		if got := commandSigningBytes(raw); string(got) != "not json" {
			t.Errorf("expected raw bytes for non-JSON, got %q", got)
		}
	})
}

// TestCommandSigningBytesRoundTrip verifies the core invariant that makes
// embedded signatures verifiable: a sender computes the signature over
// commandSigningBytes(payload) and embeds it; the receiver recomputes
// commandSigningBytes(payload) and gets identical bytes.  This is what
// allows a properly-signed command to pass verification.
func TestCommandSigningBytesRoundTrip(t *testing.T) {
	const secret = "s3cr3t"
	fields := map[string]any{
		"driver":    "opc",
		"tag":       "setpoint",
		"value":     42.0,
		"type":      core.TypeFloat64,
		"timestamp": time.Now().UnixMilli(),
	}
	signingBytes, _ := json.Marshal(fields) // canonical form (no signature)
	sig := computeHMACSignature(secret, signingBytes)
	fields["signature"] = sig
	payload, _ := json.Marshal(fields)

	// The receiver's canonical bytes must equal the sender's signingBytes.
	if got := commandSigningBytes(payload); !bytes.Equal(got, signingBytes) {
		t.Errorf("canonical bytes mismatch:\n sender: %s\nreceiver: %s", signingBytes, got)
	}
	// Therefore the signature verifies.
	if !verifyHMACSignature(secret, commandSigningBytes(payload), sig) {
		t.Error("signature should verify over canonical signing bytes")
	}
}

// ─── Timestamp extraction & freshness ───────────────────────────────

func TestExtractCommandTimestamp(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    int64
		ok      bool
	}{
		{"present", `{"timestamp":1700000000000}`, 1700000000000, true},
		{"missing", `{"driver":"opc"}`, 0, false},
		{"null", `{"timestamp":null}`, 0, false},
		{"string", `{"timestamp":"not-a-number"}`, 0, false},
		{"invalid json", `not json`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, ok := extractCommandTimestamp([]byte(tc.payload))
			if ok != tc.ok {
				t.Errorf("ok: got %v, want %v", ok, tc.ok)
			}
			if ok && ts != tc.want {
				t.Errorf("ts: got %d, want %d", ts, tc.want)
			}
		})
	}
}

func TestIsCommandTimestampFresh(t *testing.T) {
	mtr := newInitdTransport(t, "fresh", map[string]any{
		"broker":           "tcp://x:1883",
		"command-secret":   "shh",
		"command-max-skew": "5m",
	})
	now := time.Now().UnixMilli()

	cases := []struct {
		name string
		ts   int64
		want bool
	}{
		{"now", now, true},
		{"within skew future", now + 4*time.Minute.Milliseconds(), true},
		{"within skew past", now - 4*time.Minute.Milliseconds(), true},
		{"beyond skew future", now + 6*time.Minute.Milliseconds(), false},
		{"beyond skew past", now - 6*time.Minute.Milliseconds(), false},
		{"very stale", now - 60*time.Minute.Milliseconds(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mtr.isCommandTimestampFresh(tc.ts); got != tc.want {
				t.Errorf("isCommandTimestampFresh(%d): got %v, want %v", tc.ts, got, tc.want)
			}
		})
	}
}

// ─── Signed command construction helper ────────────────────────────

// buildSignedCommand constructs a command payload with a valid HMAC
// signature over the canonical signing bytes, optionally including a
// timestamp.  This mirrors how a compliant sender produces a signed,
// replay-protected command.
func buildSignedCommand(secret string, ts int64, includeTS bool) []byte {
	fields := map[string]any{
		"driver": "opc",
		"tag":    "setpoint",
		"value":  42.0,
		"type":   core.TypeFloat64,
	}
	if includeTS {
		fields["timestamp"] = ts
	}
	signingBytes, _ := json.Marshal(fields) // canonical form (no signature, sorted keys)
	sig := computeHMACSignature(secret, signingBytes)
	fields["signature"] = sig
	payload, _ := json.Marshal(fields)
	return payload
}

// ─── handleCommandMessage replay-protection integration ─────────────

func TestReplayValidFreshTimestampAccepted(t *testing.T) {
	// A signed command with a fresh timestamp is accepted and forwarded.
	mtr := newCommandTransport(t, "replay-fresh", "secret")
	payload := buildSignedCommand("secret", time.Now().UnixMilli(), true)

	mtr.handleCommandMessage("commands/opc/set", payload)

	got := drainCommand(mtr)
	if got == nil {
		t.Fatal("expected command to be forwarded for a fresh, valid signature")
	}
	if got.Tag != "setpoint" {
		t.Errorf("unexpected command tag: %q", got.Tag)
	}
}

func TestReplayStaleTimestampRejected(t *testing.T) {
	// A signed command whose timestamp is outside the ±5m skew window is
	// rejected as a replay even though its signature is valid.
	mtr := newCommandTransport(t, "replay-stale", "secret")
	stale := time.Now().Add(-10 * time.Minute).UnixMilli()
	payload := buildSignedCommand("secret", stale, true)

	mtr.handleCommandMessage("commands/opc/set", payload)

	if got := drainCommand(mtr); got != nil {
		t.Errorf("stale command must be rejected, but received %+v", got)
	}
}

func TestReplayFutureTimestampRejected(t *testing.T) {
	// A signed command with a timestamp too far in the future is also
	// rejected (guards against clock-skew abuse / pre-computed replays).
	mtr := newCommandTransport(t, "replay-future", "secret")
	future := time.Now().Add(10 * time.Minute).UnixMilli()
	payload := buildSignedCommand("secret", future, true)

	mtr.handleCommandMessage("commands/opc/set", payload)

	if got := drainCommand(mtr); got != nil {
		t.Errorf("future command must be rejected, but received %+v", got)
	}
}

func TestReplayTimestampedWrongSignatureRejected(t *testing.T) {
	// A timestamped command with an invalid signature is rejected before
	// the timestamp freshness check matters.
	mtr := newCommandTransport(t, "replay-badsig", "secret")
	fields := map[string]any{
		"driver":    "opc",
		"tag":       "setpoint",
		"value":     42.0,
		"type":      core.TypeFloat64,
		"timestamp": time.Now().UnixMilli(),
		"signature": "deadbeef",
	}
	payload, _ := json.Marshal(fields)

	mtr.handleCommandMessage("commands/opc/set", payload)

	if got := drainCommand(mtr); got != nil {
		t.Errorf("command with wrong signature must be rejected, but received %+v", got)
	}
}

func TestReplayNoTimestampBackwardCompatAccepted(t *testing.T) {
	// Backward compatibility: a signed command WITHOUT a timestamp is
	// accepted (with a warning logged) when command-strict-replay is
	// false (the default).  This preserves existing signed-command
	// senders that predate replay protection.
	mtr := newCommandTransport(t, "replay-nots-backcompat", "secret")
	payload := buildSignedCommand("secret", 0, false)

	mtr.handleCommandMessage("commands/opc/set", payload)

	got := drainCommand(mtr)
	if got == nil {
		t.Fatal("expected command to be forwarded in backward-compat mode (no timestamp, non-strict)")
	}
	if got.Tag != "setpoint" {
		t.Errorf("unexpected command tag: %q", got.Tag)
	}
}

func TestReplayNoTimestampStrictRejected(t *testing.T) {
	// In strict mode, a signed command WITHOUT a timestamp is rejected
	// even when its signature is valid — replay protection is mandatory.
	mtr := newInitdTransport(t, "replay-nots-strict", map[string]any{
		"broker":                "tcp://127.0.0.1:1883",
		"command-topic":         "commands/+/set",
		"command-secret":        "secret",
		"command-strict-replay": true,
	})
	payload := buildSignedCommand("secret", 0, false)

	mtr.handleCommandMessage("commands/opc/set", payload)

	if got := drainCommand(mtr); got != nil {
		t.Errorf("command without timestamp must be rejected in strict mode, but received %+v", got)
	}
}

func TestReplayNoTimestampStrictBadSignatureRejected(t *testing.T) {
	// In strict mode, a command without a timestamp AND a bad signature
	// is rejected (signature is verified before the strict-timestamp
	// policy, so forged commands never reach the strict check).
	mtr := newInitdTransport(t, "replay-strict-badsig", map[string]any{
		"broker":                "tcp://127.0.0.1:1883",
		"command-topic":         "commands/+/set",
		"command-secret":        "secret",
		"command-strict-replay": true,
	})
	payload, _ := json.Marshal(map[string]any{
		"driver":    "opc",
		"tag":       "setpoint",
		"value":     42.0,
		"signature": "deadbeef",
	})

	mtr.handleCommandMessage("commands/opc/set", payload)

	if got := drainCommand(mtr); got != nil {
		t.Errorf("command with bad signature must be rejected in strict mode, but received %+v", got)
	}
}

func TestReplayCustomMaxSkew(t *testing.T) {
	// A custom command-max-skew of 90s accepts a command 60s old but
	// rejects one 120s old.
	mtr := newInitdTransport(t, "replay-custom-skew", map[string]any{
		"broker":           "tcp://127.0.0.1:1883",
		"command-topic":    "commands/+/set",
		"command-secret":   "secret",
		"command-max-skew": "90s",
	})

	// 60s old — within 90s skew → accepted.
	payload := buildSignedCommand("secret", time.Now().Add(-60*time.Second).UnixMilli(), true)
	mtr.handleCommandMessage("commands/opc/set", payload)
	if got := drainCommand(mtr); got == nil {
		t.Error("command 60s old should be accepted with 90s skew")
	}

	// 120s old — beyond 90s skew → rejected.
	payload = buildSignedCommand("secret", time.Now().Add(-120*time.Second).UnixMilli(), true)
	mtr.handleCommandMessage("commands/opc/set", payload)
	if got := drainCommand(mtr); got != nil {
		t.Errorf("command 120s old should be rejected with 90s skew, but received %+v", got)
	}
}

// TestReplayDuplicateRejected verifies that the replay cache prevents the
// same authenticated command from being accepted twice within the skew
// window. This is true replay protection (not just timestamp freshness).
func TestReplayDuplicateRejected(t *testing.T) {
	mtr := newCommandTransport(t, "replay-dup", "secret")
	now := time.Now().UnixMilli()
	payload := buildSignedCommand("secret", now, true)

	// First send: should be accepted.
	mtr.handleCommandMessage("commands/opc/set", payload)
	if got := drainCommand(mtr); got == nil {
		t.Fatal("first send of valid command should be accepted")
	}

	// Second send of the exact same payload: should be rejected as duplicate.
	mtr.handleCommandMessage("commands/opc/set", payload)
	if got := drainCommand(mtr); got != nil {
		t.Errorf("duplicate command must be rejected by replay cache, but received %+v", got)
	}
}

// TestReplayDifferentCommandsBothAccepted verifies that the replay cache
// does not false-positive on different commands with different timestamps.
func TestReplayDifferentCommandsBothAccepted(t *testing.T) {
	mtr := newCommandTransport(t, "replay-diff", "secret")
	now := time.Now().UnixMilli()

	// First command.
	payload1 := buildSignedCommand("secret", now, true)
	mtr.handleCommandMessage("commands/opc/set", payload1)
	if got := drainCommand(mtr); got == nil {
		t.Fatal("first command should be accepted")
	}

	// Second command with a different timestamp (1ms later).
	payload2 := buildSignedCommand("secret", now+1, true)
	mtr.handleCommandMessage("commands/opc/set", payload2)
	if got := drainCommand(mtr); got == nil {
		t.Fatal("second different command should be accepted")
	}
}
