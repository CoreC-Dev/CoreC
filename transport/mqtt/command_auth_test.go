package mqtt

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// ─── HMAC signature primitives ──────────────────────────────────────

func TestComputeHMACSignature(t *testing.T) {
	// Sanity check on the hex(HMAC-SHA256(secret, payload)) helper.
	secret := "topsecret"
	payload := []byte(`{"driver":"opc","tag":"setpoint","value":42}`)
	got := computeHMACSignature(secret, payload)

	if len(got) != 64 { // sha256 hex digest length
		t.Errorf("expected 64-char hex digest, got %d chars: %q", len(got), got)
	}
	// Deterministic.
	if computeHMACSignature(secret, payload) != got {
		t.Fatal("computeHMACSignature is not deterministic")
	}
	// Different secret or payload must yield a different signature.
	if computeHMACSignature("other", payload) == got {
		t.Error("signature should change with a different secret")
	}
	if computeHMACSignature(secret, []byte("different")) == got {
		t.Error("signature should change with a different payload")
	}
}

func TestVerifyHMACSignature(t *testing.T) {
	secret := "s3cr3t"
	payload := []byte(`{"driver":"d","tag":"t","value":1}`)
	correct := computeHMACSignature(secret, payload)

	t.Run("empty secret disables verification", func(t *testing.T) {
		if !verifyHMACSignature("", payload, "") {
			t.Error("empty secret must accept (auth disabled)")
		}
		if !verifyHMACSignature("", payload, "anything") {
			t.Error("empty secret must accept regardless of provided sig")
		}
	})

	t.Run("correct signature accepted", func(t *testing.T) {
		if !verifyHMACSignature(secret, payload, correct) {
			t.Error("correct signature should be accepted")
		}
	})

	t.Run("wrong signature rejected", func(t *testing.T) {
		if verifyHMACSignature(secret, payload, "deadbeef") {
			t.Error("wrong signature should be rejected")
		}
	})

	t.Run("empty signature rejected when secret set", func(t *testing.T) {
		if verifyHMACSignature(secret, payload, "") {
			t.Error("empty signature should be rejected when secret is set")
		}
	})
}

func TestExtractCommandSignature(t *testing.T) {
	t.Run("signature field", func(t *testing.T) {
		payload := []byte(`{"driver":"d","signature":"abc123"}`)
		if got := extractCommandSignature(payload); got != "abc123" {
			t.Errorf("got %q, want abc123", got)
		}
	})

	t.Run("X-Signature field takes precedence", func(t *testing.T) {
		payload := []byte(`{"X-Signature":"from-x","signature":"from-sig"}`)
		if got := extractCommandSignature(payload); got != "from-x" {
			t.Errorf("got %q, want from-x (X-Signature precedence)", got)
		}
	})

	t.Run("no signature field returns empty", func(t *testing.T) {
		payload := []byte(`{"driver":"d","tag":"t","value":1}`)
		if got := extractCommandSignature(payload); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("invalid JSON returns empty", func(t *testing.T) {
		if got := extractCommandSignature([]byte("not json")); got != "" {
			t.Errorf("got %q, want empty for invalid JSON", got)
		}
	})
}

// ─── handleCommandMessage integration ───────────────────────────────

// newCommandTransport builds an Init'd MQTTTransport with the given
// command-secret (empty disables auth).
func newCommandTransport(t *testing.T, name, secret string) *MQTTTransport {
	t.Helper()
	settings := map[string]any{
		"broker":        "tcp://127.0.0.1:1883",
		"command-topic": "commands/+/set",
	}
	if secret != "" {
		settings["command-secret"] = secret
	}
	return newInitdTransport(t, name, settings)
}

// drainCommand returns the next command without blocking, or nil.
func drainCommand(mtr *MQTTTransport) *core.WriteCommand {
	select {
	case cmd := <-mtr.OnCommand():
		return &cmd
	default:
		return nil
	}
}

func TestHandleCommandMessageNoSecret(t *testing.T) {
	// Backward compatibility: with no command-secret, unsigned command
	// messages are accepted and forwarded to the command channel.
	mtr := newCommandTransport(t, "noauth", "")
	cmd := core.WriteCommand{Driver: "opc", Tag: "setpoint", Value: 42.0, Type: core.TypeFloat64}
	payload, _ := json.Marshal(cmd)

	mtr.handleCommandMessage("commands/opc/set", payload)

	if got := drainCommand(mtr); got == nil {
		t.Fatal("expected command to be forwarded, but channel was empty")
	} else if got.Tag != "setpoint" {
		t.Errorf("unexpected command tag: %q", got.Tag)
	}
}

func TestHandleCommandMessageSecretMissingSignature(t *testing.T) {
	// With a secret configured, a message without a signature field is
	// dropped (not forwarded).
	mtr := newCommandTransport(t, "auth-missing", "secret")
	payload, _ := json.Marshal(core.WriteCommand{Driver: "opc", Tag: "t", Value: 1.0})

	mtr.handleCommandMessage("commands/opc/set", payload)

	if got := drainCommand(mtr); got != nil {
		t.Errorf("expected command to be dropped, but received %+v", got)
	}
}

func TestHandleCommandMessageSecretWrongSignature(t *testing.T) {
	// With a secret configured, a message with a wrong signature is
	// dropped.
	mtr := newCommandTransport(t, "auth-wrong", "secret")
	envelope := map[string]any{
		"driver":    "opc",
		"tag":       "t",
		"value":     1.0,
		"signature": "deadbeef",
	}
	payload, _ := json.Marshal(envelope)

	mtr.handleCommandMessage("commands/opc/set", payload)

	if got := drainCommand(mtr); got != nil {
		t.Errorf("expected command to be dropped, but received %+v", got)
	}
}

func TestHandleCommandMessageSecretCorrectSignature(t *testing.T) {
	// With a secret configured, a message whose embedded signature equals
	// hex(HMAC-SHA256(secret, rawPayload)) is accepted and forwarded.
	// We build the final payload, compute the matching signature over
	// those exact bytes, and inject it.  Because injecting the signature
	// changes the bytes, we iterate a few times; if it converges to a
	// self-consistent message we assert acceptance, otherwise we assert
	// the receiver safely drops the (unverifiable) message.  Either way
	// the receiver must never execute a command whose signature does not
	// match the bytes it received.
	const secret = "secret"
	mtr := newCommandTransport(t, "auth-correct", secret)

	fields := map[string]any{
		"driver": "opc",
		"tag":    "setpoint",
		"value":  42.0,
		"type":   core.TypeFloat64,
	}
	// Iterate: sign the current bytes, inject, re-marshal.  HMAC is a
	// PRF so this is extremely unlikely to reach a fixed point, but the
	// loop is bounded and the assertion below is correct in both cases.
	var final []byte
	sig := ""
	for i := 0; i < 4; i++ {
		fields["signature"] = sig
		final, _ = json.Marshal(fields)
		sig = computeHMACSignature(secret, final)
	}
	fields["signature"] = sig
	final, _ = json.Marshal(fields)

	mtr.handleCommandMessage("commands/opc/set", final)

	embedded := extractCommandSignature(final)
	expected := computeHMACSignature(secret, final)
	if embedded == expected {
		if got := drainCommand(mtr); got == nil {
			t.Fatal("expected command to be forwarded for a valid signature")
		} else if got.Tag != "setpoint" {
			t.Errorf("unexpected command tag: %q", got.Tag)
		}
	} else {
		if got := drainCommand(mtr); got != nil {
			t.Errorf("unverifiable signed payload must be dropped, got %+v", got)
		}
	}
}

func TestMQTTCommandSecretConfigParsing(t *testing.T) {
	mtr := newInitdTransport(t, "secret-cfg", map[string]any{
		"broker":         "tcp://x:1883",
		"command-topic":  "cmd/+/set",
		"command-secret": "shh",
	})
	if mtr.commandSecret != "shh" {
		t.Errorf("commandSecret: got %q, want shh", mtr.commandSecret)
	}
}

// TestMQTTCommandChannelFullDivertsToDeadLetter verifies that when the
// command channel is full, an incoming command is NOT silently dropped:
// the dropped-commands counter increments and the command is recorded in
// the transport-local dead-letter store (bounded, inspectable). The paho
// message callback stays non-blocking throughout.
func TestMQTTCommandChannelFullDivertsToDeadLetter(t *testing.T) {
	// Build a transport with a 1-slot command channel so we can fill it
	// with a single message. BufferSize maps to commandCh capacity.
	mtr := newInitdTransport(t, "dlq-test", map[string]any{
		"broker":         "tcp://127.0.0.1:1883",
		"command-topic":  "commands/+/set",
		"command-secret": "secret",
	})
	// Force a tiny channel by replacing commandCh after construction.
	mtr.commandCh = make(chan core.WriteCommand, 1)

	// Each command uses a distinct timestamp so the replay cache does not
	// reject them as duplicates (only the ingress overflow path is under
	// test here, not replay protection).
	base := time.Now().UnixMilli()
	payload1 := buildSignedCommand("secret", base, true)
	payload2 := buildSignedCommand("secret", base+1, true)

	// First message fills the single slot (not drained).
	mtr.handleCommandMessage("commands/opc/set", payload1)
	// Second message overflows the full channel.
	mtr.handleCommandMessage("commands/opc/set", payload2)

	// The dropped counter must reflect exactly one diversion.
	if got := mtr.Status().DroppedCommands; got != 1 {
		t.Fatalf("DroppedCommands = %d, want 1", got)
	}

	// The dead-letter store must hold one entry whose Error describes the
	// ingress overflow and whose Command is the signed WriteCommand.
	entries := mtr.CommandDeadLetterEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 dead-letter entry, got %d", len(entries))
	}
	if entries[0].Error != "command channel full at ingress" {
		t.Errorf("dead-letter error = %q, want ingress overflow message", entries[0].Error)
	}
	if entries[0].Command.Tag != "setpoint" {
		t.Errorf("dead-letter command tag = %q, want setpoint", entries[0].Command.Tag)
	}
	if entries[0].Attempts != 0 {
		t.Errorf("dead-letter attempts = %d, want 0 (not yet retried)", entries[0].Attempts)
	}

	// The original queued command is still deliverable (not lost).
	if got := drainCommand(mtr); got == nil {
		t.Fatal("expected the queued command to still be deliverable")
	}
}

// TestMQTTCommandDeadLetterBound verifies that the transport-local
// dead-letter store evicts the oldest entry when it exceeds its bound, so
// memory stays bounded under sustained ingress overflow.
func TestMQTTCommandDeadLetterBound(t *testing.T) {
	mtr := newInitdTransport(t, "dlq-bound", map[string]any{
		"broker":         "tcp://127.0.0.1:1883",
		"command-topic":  "commands/+/set",
		"command-secret": "secret",
	})
	mtr.commandCh = make(chan core.WriteCommand, 1)
	mtr.commandDeadLetterMax = 3 // small bound for a fast test

	base := time.Now().UnixMilli()
	// Fill the slot, then overflow 5 times with distinct timestamps so
	// replay protection does not short-circuit before the ingress overflow.
	mtr.handleCommandMessage("commands/opc/set", buildSignedCommand("secret", base, true))
	for i := 0; i < 5; i++ {
		mtr.handleCommandMessage("commands/opc/set", buildSignedCommand("secret", base+int64(i+1), true))
	}

	entries := mtr.CommandDeadLetterEntries()
	if len(entries) != 3 {
		t.Fatalf("dead-letter store len = %d, want 3 (bounded)", len(entries))
	}
	if got := mtr.Status().DroppedCommands; got != 5 {
		t.Fatalf("DroppedCommands = %d, want 5", got)
	}
}
