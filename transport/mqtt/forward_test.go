// Package mqtt implements the MQTT transport for CoreC.
package mqtt

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// These tests exercise ForwardCommand logic (config validation, topic
// routing, and HMAC signing) using a mock paho client. They do NOT require
// a running MQTT broker, so they must never be skipped.

// ─── Mock paho client ───────────────────────────────────────────────

// mockToken is a pahomqtt.Token stub that reports a configurable result.
type mockToken struct {
	doneCh chan struct{}
	err    error
}

func newMockToken(err error) *mockToken {
	t := &mockToken{doneCh: make(chan struct{}), err: err}
	close(t.doneCh)
	return t
}

func (t *mockToken) Wait() bool                     { <-t.doneCh; return true }
func (t *mockToken) WaitTimeout(time.Duration) bool { <-t.doneCh; return true }
func (t *mockToken) Done() <-chan struct{}          { return t.doneCh }
func (t *mockToken) Error() error                   { return t.err }

// publishedMessage records a single Publish call.
type publishedMessage struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
}

// mockPahoClient is a pahomqtt.Client stub that records Publish calls and
// reports a configured connected state. Only the methods exercised by
// ForwardCommand (IsConnected, Publish) carry real behaviour; the rest
// return zero values / nil tokens so the stub satisfies the interface
// without depending on a broker.
type mockPahoClient struct {
	mu         sync.Mutex
	connected  bool
	publishes  []publishedMessage
	publishErr error
}

func (c *mockPahoClient) IsConnected() bool      { return c.connected }
func (c *mockPahoClient) IsConnectionOpen() bool { return c.connected }
func (c *mockPahoClient) Connect() pahomqtt.Token {
	return newMockToken(nil)
}
func (c *mockPahoClient) Disconnect(quiesce uint) {}

func (c *mockPahoClient) Publish(topic string, qos byte, retained bool, payload interface{}) pahomqtt.Token {
	c.mu.Lock()
	c.publishes = append(c.publishes, publishedMessage{
		topic:    topic,
		qos:      qos,
		retained: retained,
		payload:  payload.([]byte),
	})
	err := c.publishErr
	c.mu.Unlock()
	return newMockToken(err)
}

func (c *mockPahoClient) Subscribe(topic string, qos byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return newMockToken(nil)
}
func (c *mockPahoClient) SubscribeMultiple(filters map[string]byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return newMockToken(nil)
}
func (c *mockPahoClient) Unsubscribe(topics ...string) pahomqtt.Token { return newMockToken(nil) }
func (c *mockPahoClient) AddRoute(topic string, callback pahomqtt.MessageHandler) {
}
func (c *mockPahoClient) OptionsReader() pahomqtt.ClientOptionsReader {
	return pahomqtt.ClientOptionsReader{}
}

// drainPublished returns a snapshot of all recorded Publish calls.
func (c *mockPahoClient) drainPublished() []publishedMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]publishedMessage, len(c.publishes))
	copy(out, c.publishes)
	return out
}

// newForwardTransport builds and Inits an MQTTTransport with the given
// forward settings, then injects a connected mock paho client so
// ForwardCommand can publish without a real broker.
func newForwardTransport(t *testing.T, settings map[string]any, connected bool) (*MQTTTransport, *mockPahoClient) {
	t.Helper()
	mtr := newInitdTransport(t, "fwd-mqtt", settings)
	client := &mockPahoClient{connected: connected}
	mtr.client = client
	return mtr, client
}

// ─── Tests ──────────────────────────────────────────────────────────

// TestForwardCommandNoForwardTopic verifies that ForwardCommand returns a
// descriptive error (mentioning the transport name) when no
// command-forward-topic is configured.
func TestForwardCommandNoForwardTopic(t *testing.T) {
	mtr := newInitdTransport(t, "nofwd-mqtt", map[string]any{
		"broker": "tcp://127.0.0.1:1883",
	})
	// Inject a connected client so the failure is attributable to the
	// missing topic, not the connection state.
	mtr.client = &mockPahoClient{connected: true}

	err := mtr.ForwardCommand(context.Background(), core.WriteCommand{
		Driver: "opc", Tag: "setpoint", Value: 42.0,
	})
	if err == nil {
		t.Fatal("ForwardCommand without command-forward-topic: got nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "no command-forward-topic") {
		t.Errorf("error %q should mention 'no command-forward-topic'", err.Error())
	}
	if !strings.Contains(err.Error(), "nofwd-mqtt") {
		t.Errorf("error %q should mention the transport name 'nofwd-mqtt'", err.Error())
	}
}

// TestForwardCommandNotConnected verifies that ForwardCommand returns a
// "not connected" error when the underlying MQTT client is not connected,
// even when a command-forward-topic is configured.
func TestForwardCommandNotConnected(t *testing.T) {
	mtr, client := newForwardTransport(t, map[string]any{
		"broker":                "tcp://127.0.0.1:1883",
		"command-forward-topic": "corec/commands/forward",
	}, false)
	if client.IsConnected() {
		t.Fatal("mock client should report disconnected")
	}

	err := mtr.ForwardCommand(context.Background(), core.WriteCommand{
		Driver: "opc", Tag: "setpoint", Value: 42.0,
	})
	if err == nil {
		t.Fatal("ForwardCommand when disconnected: got nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error %q should mention 'not connected'", err.Error())
	}
}

// TestForwardCommandPublishesToCorrectTopic verifies that, when unsigned,
// ForwardCommand publishes the marshalled command to the configured
// command-forward-topic and increments the published counter.
func TestForwardCommandPublishesToCorrectTopic(t *testing.T) {
	mtr, client := newForwardTransport(t, map[string]any{
		"broker":                "tcp://127.0.0.1:1883",
		"command-forward-topic": "corec/relay/commands",
	}, true)

	cmd := core.WriteCommand{
		Driver: "opc-plc",
		Tag:    "setpoint",
		Value:  7.5,
		Type:   core.TypeFloat64,
	}

	if err := mtr.ForwardCommand(context.Background(), cmd); err != nil {
		t.Fatalf("ForwardCommand failed: %v", err)
	}

	pub := client.drainPublished()
	if len(pub) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub))
	}
	if pub[0].topic != "corec/relay/commands" {
		t.Errorf("publish topic: got %q, want %q", pub[0].topic, "corec/relay/commands")
	}
	if pub[0].retained {
		t.Errorf("forwarded command must not be retained, got retained=true")
	}

	// Payload must be the exact JSON of the command (no signing fields).
	var got core.WriteCommand
	if err := json.Unmarshal(pub[0].payload, &got); err != nil {
		t.Fatalf("payload is not valid WriteCommand JSON: %v", err)
	}
	if got.Driver != cmd.Driver || got.Tag != cmd.Tag {
		t.Errorf("forwarded command mismatch: got %+v, want driver=%q tag=%q", got, cmd.Driver, cmd.Tag)
	}
	// Ensure no signing/timestamp fields were added (unsigned path).
	if hasField(pub[0].payload, "X-Signature") || hasField(pub[0].payload, "timestamp") {
		t.Errorf("unsigned forwarded command must not carry signature/timestamp fields: %s", pub[0].payload)
	}

	if st := mtr.Status(); st.Published != 1 {
		t.Errorf("Published counter: got %d, want 1", st.Published)
	}
}

// TestForwardCommandSignsWhenSecretSet verifies that, when a
// command-forward-secret is configured, ForwardCommand embeds a fresh
// timestamp and a valid X-Signature that the downstream node can verify
// with the same secret over the canonical signing bytes.
func TestForwardCommandSignsWhenSecretSet(t *testing.T) {
	secret := "relay-shared-secret"
	mtr, client := newForwardTransport(t, map[string]any{
		"broker":                 "tcp://127.0.0.1:1883",
		"command-forward-topic":  "corec/relay/commands",
		"command-forward-secret": secret,
	}, true)

	cmd := core.WriteCommand{
		Driver: "opc-plc",
		Tag:    "setpoint",
		Value:  9.0,
		Type:   core.TypeFloat64,
	}

	if err := mtr.ForwardCommand(context.Background(), cmd); err != nil {
		t.Fatalf("ForwardCommand failed: %v", err)
	}

	pub := client.drainPublished()
	if len(pub) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub))
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(pub[0].payload, &m); err != nil {
		t.Fatalf("payload is not a JSON object: %v", err)
	}

	// A timestamp must be present.
	tsRaw, ok := m["timestamp"]
	if !ok {
		t.Fatal("signed forwarded command must include a 'timestamp' field")
	}
	var ts int64
	if err := json.Unmarshal(tsRaw, &ts); err != nil {
		t.Fatalf("timestamp is not a number: %v", err)
	}
	if ts <= 0 {
		t.Errorf("timestamp should be a positive Unix-ms value, got %d", ts)
	}

	// An X-Signature must be present and valid.
	sigRaw, ok := m["X-Signature"]
	if !ok {
		t.Fatal("signed forwarded command must include an 'X-Signature' field")
	}
	var sig string
	if err := json.Unmarshal(sigRaw, &sig); err != nil {
		t.Fatalf("X-Signature is not a string: %v", err)
	}

	// The signature must verify against the canonical signing bytes
	// (payload with signature fields removed) using the configured secret.
	if !verifyHMACSignature(secret, commandSigningBytes(pub[0].payload), sig) {
		t.Error("forwarded command X-Signature does not verify under the configured secret")
	}

	// The signature must be over the exact bytes carrying the timestamp,
	// so a downstream node recomputing commandSigningBytes must match.
	if computeHMACSignature(secret, commandSigningBytes(pub[0].payload)) != sig {
		t.Error("recomputed signature does not match the embedded X-Signature")
	}
}

// TestForwardCommandDifferentSecretRejects verifies that a signature
// produced under one secret does NOT verify under a different secret —
// i.e. the signing is secret-dependent (authenticates the sender).
func TestForwardCommandDifferentSecretRejects(t *testing.T) {
	mtr, client := newForwardTransport(t, map[string]any{
		"broker":                 "tcp://127.0.0.1:1883",
		"command-forward-topic":  "corec/relay/commands",
		"command-forward-secret": "secret-a",
	}, true)

	if err := mtr.ForwardCommand(context.Background(), core.WriteCommand{
		Driver: "opc", Tag: "t", Value: 1.0,
	}); err != nil {
		t.Fatalf("ForwardCommand failed: %v", err)
	}

	pub := client.drainPublished()
	if len(pub) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub))
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(pub[0].payload, &m); err != nil {
		t.Fatalf("payload not JSON object: %v", err)
	}
	var sig string
	_ = json.Unmarshal(m["X-Signature"], &sig)

	if verifyHMACSignature("secret-b", commandSigningBytes(pub[0].payload), sig) {
		t.Error("signature signed with secret-a must NOT verify under secret-b")
	}
}

// TestForwardCommandInitParsesForwardSettings verifies that Init reads the
// command-forward-topic and command-forward-secret settings into the
// transport struct fields.
func TestForwardCommandInitParsesForwardSettings(t *testing.T) {
	mtr := newInitdTransport(t, "cfg-fwd-mqtt", map[string]any{
		"broker":                 "tcp://127.0.0.1:1883",
		"command-forward-topic":  "corec/fwd",
		"command-forward-secret": "s3cret",
	})

	if mtr.commandForwardTopic != "corec/fwd" {
		t.Errorf("commandForwardTopic: got %q, want %q", mtr.commandForwardTopic, "corec/fwd")
	}
	if mtr.commandForwardSecret != "s3cret" {
		t.Errorf("commandForwardSecret: got %q, want %q", mtr.commandForwardSecret, "s3cret")
	}
}

// TestMQTTTransportImplementsCommandForwarder verifies that *MQTTTransport
// satisfies the core.CommandForwarder optional capability interface, so the
// engine's type-assertion in forwardCommand picks it up.
func TestMQTTTransportImplementsCommandForwarder(t *testing.T) {
	var _ core.CommandForwarder = (*MQTTTransport)(nil)
}

// hasField reports whether the JSON object in payload contains the given
// top-level key.
func hasField(payload []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
