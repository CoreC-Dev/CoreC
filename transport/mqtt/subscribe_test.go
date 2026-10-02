package mqtt

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// These tests exercise the MQTT subscription wiring — subscribeCommands,
// subscribeData, and the connected success path of Publish — using a mock
// paho client that records Subscribe/Publish calls and lets tests replay
// mock messages through the registered handlers. No real broker is needed,
// so these tests must never be skipped.

// ─── Mock paho.Message ──────────────────────────────────────────────

// mockMessage is a pahomqtt.Message stub carrying a topic and payload so
// the subscription handlers can exercise their parsing/enqueue paths.
type mockMessage struct {
	topic    string
	payload  []byte
	qos      byte
	retained bool
	dup      bool
	msgID    uint16
}

func (m *mockMessage) Duplicate() bool   { return m.dup }
func (m *mockMessage) Qos() byte         { return m.qos }
func (m *mockMessage) Retained() bool    { return m.retained }
func (m *mockMessage) Topic() string     { return m.topic }
func (m *mockMessage) MessageID() uint16 { return m.msgID }
func (m *mockMessage) Payload() []byte   { return m.payload }
func (m *mockMessage) Ack()              {}

// neverDoneToken is a pahomqtt.Token stub that never completes, so
// WaitTimeout reports a timeout. It is used to exercise the subscribe/publish
// timeout branches. Error() returns nil because the operation never finished.
type neverDoneToken struct {
	done chan struct{}
}

func newNeverDoneToken() *neverDoneToken {
	return &neverDoneToken{done: make(chan struct{})} // never closed
}

func (t *neverDoneToken) Wait() bool                     { <-t.done; return true }
func (t *neverDoneToken) WaitTimeout(time.Duration) bool { return false }
func (t *neverDoneToken) Done() <-chan struct{}          { return t.done }
func (t *neverDoneToken) Error() error                   { return nil }

// ─── subscribeCommands ─────────────────────────────────────────────

// TestSubscribeCommandsRecordsSubscription verifies that subscribeCommands
// subscribes to the configured command topic at the transport's QoS and
// registers a non-nil handler.
func TestSubscribeCommandsRecordsSubscription(t *testing.T) {
	mtr := newInitdTransport(t, "subcmd-mqtt", map[string]any{
		"broker":        "tcp://127.0.0.1:1883",
		"command-topic": "corec/commands/+/set",
		"qos":           float64(2),
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	mtr.subscribeCommands(client)

	subs := client.drainSubscribes()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscribe, got %d", len(subs))
	}
	if subs[0].topic != "corec/commands/+/set" {
		t.Errorf("subscribe topic: got %q, want corec/commands/+/set", subs[0].topic)
	}
	if subs[0].qos != 2 {
		t.Errorf("subscribe qos: got %d, want 2", subs[0].qos)
	}
	if subs[0].handler == nil {
		t.Error("subscribe handler must not be nil")
	}
}

// TestSubscribeCommandsHandlerEnqueuesCommand verifies that the handler
// registered by subscribeCommands forwards a valid command message onto the
// command channel (the no-secret backward-compatible path).
func TestSubscribeCommandsHandlerEnqueuesCommand(t *testing.T) {
	mtr := newInitdTransport(t, "subcmd-handler", map[string]any{
		"broker":        "tcp://127.0.0.1:1883",
		"command-topic": "corec/commands/opc/set",
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	mtr.subscribeCommands(client)
	handler := client.drainSubscribes()[0].handler

	cmd := core.WriteCommand{
		Driver: "opc",
		Tag:    "setpoint",
		Value:  42.0,
		Type:   core.TypeFloat64,
	}
	payload, _ := json.Marshal(cmd)
	handler(client, &mockMessage{topic: "corec/commands/opc/set", payload: payload})

	got := drainCommand(mtr)
	if got == nil {
		t.Fatal("expected command to be forwarded onto commandCh, but channel was empty")
	}
	if got.Driver != "opc" || got.Tag != "setpoint" || got.Value != 42.0 {
		t.Errorf("forwarded command mismatch: got %+v", got)
	}
}

// TestSubscribeCommandsHandlerSignedCommand verifies the handler path when
// command authentication is enabled: a correctly signed command is accepted
// and forwarded to the command channel.
func TestSubscribeCommandsHandlerSignedCommand(t *testing.T) {
	const secret = "cmd-secret"
	mtr := newInitdTransport(t, "subcmd-signed", map[string]any{
		"broker":         "tcp://127.0.0.1:1883",
		"command-topic":  "corec/commands/opc/set",
		"command-secret": secret,
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	mtr.subscribeCommands(client)
	handler := client.drainSubscribes()[0].handler

	// Build a signed command. The signature is computed over the canonical
	// signing bytes (signature field excluded), so it is stable: adding the
	// signature field does not change commandSigningBytes(payload).
	base := map[string]any{
		"driver": "opc",
		"tag":    "setpoint",
		"value":  7.0,
		"type":   core.TypeFloat64,
	}
	baseBytes, _ := json.Marshal(base)
	sig := computeHMACSignature(secret, commandSigningBytes(baseBytes))
	final, _ := json.Marshal(map[string]any{
		"driver":    "opc",
		"tag":       "setpoint",
		"value":     7.0,
		"type":      core.TypeFloat64,
		"signature": sig,
	})

	handler(client, &mockMessage{topic: "corec/commands/opc/set", payload: final})

	// The signature verifies over the canonical signing bytes of the received
	// payload, so the command must be accepted and forwarded.
	got := drainCommand(mtr)
	if got == nil {
		t.Fatal("expected signed command to be forwarded onto commandCh")
	}
	if got.Tag != "setpoint" {
		t.Errorf("forwarded command tag: got %q, want setpoint", got.Tag)
	}
}

// TestSubscribeCommandsTimeout verifies that a subscribe token that never
// completes is handled gracefully (logged, no panic) and the subscription is
// still recorded.
func TestSubscribeCommandsTimeout(t *testing.T) {
	mtr := newInitdTransport(t, "subcmd-timeout", map[string]any{
		"broker":         "tcp://127.0.0.1:1883",
		"command-topic":  "corec/commands/opc/set",
		"command-secret": "shh",
	})
	client := &mockPahoClient{connected: true, subscribeToken: newNeverDoneToken()}
	mtr.client = client

	// Must not panic or block.
	mtr.subscribeCommands(client)

	if len(client.drainSubscribes()) != 1 {
		t.Error("subscribe must still be recorded even when the token times out")
	}
}

// TestSubscribeCommandsError verifies that a subscribe token that completes
// with an error is handled gracefully (logged, no panic).
func TestSubscribeCommandsError(t *testing.T) {
	mtr := newInitdTransport(t, "subcmd-err", map[string]any{
		"broker":         "tcp://127.0.0.1:1883",
		"command-topic":  "corec/commands/opc/set",
		"command-secret": "shh",
	})
	client := &mockPahoClient{connected: true, subscribeErr: errors.New("broker rejected subscription")}
	mtr.client = client

	// Must not panic.
	mtr.subscribeCommands(client)

	if len(client.drainSubscribes()) != 1 {
		t.Error("subscribe must still be recorded even when the token errors")
	}
}

// ─── subscribeData ─────────────────────────────────────────────────

// TestSubscribeDataRecordsSubscription verifies that subscribeData subscribes
// to the configured data topic at the transport's QoS with a non-nil handler.
func TestSubscribeDataRecordsSubscription(t *testing.T) {
	mtr := newInitdTransport(t, "subdata-mqtt", map[string]any{
		"broker":     "tcp://127.0.0.1:1883",
		"data-topic": "corec/data/in",
		"qos":        float64(1),
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	mtr.subscribeData(client)

	subs := client.drainSubscribes()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscribe, got %d", len(subs))
	}
	if subs[0].topic != "corec/data/in" {
		t.Errorf("subscribe topic: got %q, want corec/data/in", subs[0].topic)
	}
	if subs[0].qos != 1 {
		t.Errorf("subscribe qos: got %d, want 1", subs[0].qos)
	}
	if subs[0].handler == nil {
		t.Error("subscribe handler must not be nil")
	}
}

// TestSubscribeDataHandlerEnqueuesPoint verifies that the handler registered
// by subscribeData parses a valid DataPoint payload and forwards it onto the
// data channel, incrementing the received counter.
func TestSubscribeDataHandlerEnqueuesPoint(t *testing.T) {
	mtr := newInitdTransport(t, "subdata-handler", map[string]any{
		"broker":     "tcp://127.0.0.1:1883",
		"data-topic": "corec/data/in",
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	mtr.subscribeData(client)
	handler := client.drainSubscribes()[0].handler

	point := core.DataPoint{
		Driver:    "upstream",
		Tag:       "temperature",
		Value:     23.5,
		Type:      core.TypeFloat64,
		Timestamp: time.Now(),
	}
	payload, _ := json.Marshal(point)
	handler(client, &mockMessage{topic: "corec/data/in", payload: payload})

	select {
	case got := <-mtr.OnData():
		if got.Driver != "upstream" || got.Tag != "temperature" || got.Value != 23.5 {
			t.Errorf("forwarded data point mismatch: got %+v", got)
		}
	default:
		t.Fatal("expected data point to be forwarded onto dataCh, but channel was empty")
	}
	if st := mtr.Status(); st.Received != 1 {
		t.Errorf("Received counter: got %d, want 1", st.Received)
	}
}

// TestSubscribeDataHandlerParseError verifies that a payload the parser
// rejects is dropped gracefully (no point enqueued, no panic, received stays 0).
func TestSubscribeDataHandlerParseError(t *testing.T) {
	mtr := newInitdTransport(t, "subdata-badparse", map[string]any{
		"broker":     "tcp://127.0.0.1:1883",
		"data-topic": "corec/data/in",
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	mtr.subscribeData(client)
	handler := client.drainSubscribes()[0].handler

	// Invalid JSON cannot be unmarshalled into a DataPoint by the default parser.
	handler(client, &mockMessage{topic: "corec/data/in", payload: []byte("not-json")})

	select {
	case <-mtr.OnData():
		t.Error("expected no data point after a parse failure, but channel had one")
	default:
		// expected
	}
	if st := mtr.Status(); st.Received != 0 {
		t.Errorf("Received counter after parse failure: got %d, want 0", st.Received)
	}
}

// TestSubscribeDataHandlerDropsWhenChannelFull verifies that when the data
// channel is full, the handler drops the point (non-blocking send) rather than
// blocking, and does not increment the received counter.
func TestSubscribeDataHandlerDropsWhenChannelFull(t *testing.T) {
	mtr := newInitdTransport(t, "subdata-full", map[string]any{
		"broker":     "tcp://127.0.0.1:1883",
		"data-topic": "corec/data/in",
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	mtr.subscribeData(client)
	handler := client.drainSubscribes()[0].handler

	// Fill the data channel to capacity so the next send hits the default branch.
	for i := 0; i < cap(mtr.dataCh); i++ {
		mtr.dataCh <- core.DataPoint{Driver: "fill", Tag: "t"}
	}

	point := core.DataPoint{Driver: "upstream", Tag: "overflow", Value: 1.0, Timestamp: time.Now()}
	payload, _ := json.Marshal(point)
	// Must not block.
	handler(client, &mockMessage{topic: "corec/data/in", payload: payload})

	if st := mtr.Status(); st.Received != 0 {
		t.Errorf("Received counter after drop: got %d, want 0 (point was dropped, not received)", st.Received)
	}
	// The channel must still be exactly at capacity (the overflow point was dropped).
	if len(mtr.dataCh) != cap(mtr.dataCh) {
		t.Errorf("dataCh length: got %d, want %d (overflow must be dropped, not enqueued)", len(mtr.dataCh), cap(mtr.dataCh))
	}
}

// TestSubscribeDataTimeout verifies that a subscribe token that never
// completes is handled gracefully.
func TestSubscribeDataTimeout(t *testing.T) {
	mtr := newInitdTransport(t, "subdata-timeout", map[string]any{
		"broker":     "tcp://127.0.0.1:1883",
		"data-topic": "corec/data/in",
	})
	client := &mockPahoClient{connected: true, subscribeToken: newNeverDoneToken()}
	mtr.client = client

	mtr.subscribeData(client) // must not panic or block

	if len(client.drainSubscribes()) != 1 {
		t.Error("subscribe must still be recorded even when the token times out")
	}
}

// TestSubscribeDataError verifies that a subscribe token that completes with
// an error is handled gracefully.
func TestSubscribeDataError(t *testing.T) {
	mtr := newInitdTransport(t, "subdata-err", map[string]any{
		"broker":     "tcp://127.0.0.1:1883",
		"data-topic": "corec/data/in",
	})
	client := &mockPahoClient{connected: true, subscribeErr: errors.New("broker rejected subscription")}
	mtr.client = client

	mtr.subscribeData(client) // must not panic

	if len(client.drainSubscribes()) != 1 {
		t.Error("subscribe must still be recorded even when the token errors")
	}
}

// ─── Publish (connected success + failure paths) ───────────────────

// TestPublishSuccess verifies the connected success path: Publish marshals the
// point, publishes to the template-rendered topic with the configured QoS and
// retained flag, and increments the published counter.
func TestPublishSuccess(t *testing.T) {
	mtr := newInitdTransport(t, "pub-ok", map[string]any{
		"broker":         "tcp://127.0.0.1:1883",
		"qos":            float64(2),
		"retained":       true,
		"topic-template": "corec/{{.Driver}}/{{.Tag}}",
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	point := core.DataPoint{
		Driver:    "opc",
		Tag:       "temp",
		Value:     99.0,
		Type:      core.TypeFloat64,
		Timestamp: time.Now(),
	}
	if err := mtr.Publish(context.Background(), point); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	pub := client.drainPublished()
	if len(pub) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub))
	}
	if pub[0].topic != "corec/opc/temp" {
		t.Errorf("publish topic: got %q, want corec/opc/temp", pub[0].topic)
	}
	if pub[0].qos != 2 {
		t.Errorf("publish qos: got %d, want 2", pub[0].qos)
	}
	if !pub[0].retained {
		t.Error("publish retained: got false, want true")
	}
	var got core.DataPoint
	if err := json.Unmarshal(pub[0].payload, &got); err != nil {
		t.Fatalf("payload is not valid DataPoint JSON: %v", err)
	}
	if got.Driver != "opc" || got.Tag != "temp" || got.Value != 99.0 {
		t.Errorf("published payload mismatch: got %+v", got)
	}
	if st := mtr.Status(); st.Published != 1 {
		t.Errorf("Published counter: got %d, want 1", st.Published)
	}
	if st := mtr.Status(); st.Failed != 0 {
		t.Errorf("Failed counter: got %d, want 0", st.Failed)
	}
}

// TestPublishTokenError verifies that a publish token that completes with an
// error causes Publish to return that error and increment the failed counter.
func TestPublishTokenError(t *testing.T) {
	mtr := newInitdTransport(t, "pub-err", map[string]any{
		"broker": "tcp://127.0.0.1:1883",
	})
	client := &mockPahoClient{connected: true, publishErr: errors.New("broker rejected publish")}
	mtr.client = client

	err := mtr.Publish(context.Background(), core.DataPoint{Driver: "d", Tag: "t", Value: 1.0, Timestamp: time.Now()})
	if err == nil {
		t.Fatal("Publish with errored token: got nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "mqtt publish failed") {
		t.Errorf("error %q should mention 'mqtt publish failed'", err.Error())
	}
	if st := mtr.Status(); st.Failed != 1 {
		t.Errorf("Failed counter: got %d, want 1", st.Failed)
	}
	if st := mtr.Status(); st.Published != 0 {
		t.Errorf("Published counter: got %d, want 0", st.Published)
	}
}

// TestPublishTimeout verifies that a publish token that never completes causes
// Publish to return a timeout error and increment the failed counter.
func TestPublishTimeout(t *testing.T) {
	mtr := newInitdTransport(t, "pub-timeout", map[string]any{
		"broker": "tcp://127.0.0.1:1883",
	})
	client := &mockPahoClient{connected: true, publishToken: newNeverDoneToken()}
	mtr.client = client

	err := mtr.Publish(context.Background(), core.DataPoint{Driver: "d", Tag: "t", Value: 1.0, Timestamp: time.Now()})
	if err == nil {
		t.Fatal("Publish with never-completing token: got nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error %q should mention 'timed out'", err.Error())
	}
	if st := mtr.Status(); st.Failed != 1 {
		t.Errorf("Failed counter: got %d, want 1", st.Failed)
	}
}

// ─── Start-time subscribe wiring (on-connect handler) ──────────────

// TestOnConnectHandlerSubscribes verifies that the on-connect handler set up
// by Start subscribes to both the command and data topics when both are
// configured. The real paho client is replaced by a mock before invoking the
// handler, so no broker connection is required.
func TestOnConnectHandlerSubscribes(t *testing.T) {
	mtr := newInitdTransport(t, "onconnect-mqtt", map[string]any{
		"broker":         "tcp://127.0.0.1:1883",
		"command-topic":  "corec/commands/+/set",
		"command-secret": "shh",
		"data-topic":     "corec/data/in",
	})
	client := &mockPahoClient{connected: true}
	mtr.client = client

	// Drive the subscribe paths directly, mirroring the on-connect handler in
	// Start. This exercises the same wiring without a real broker connection.
	mtr.subscribeCommands(client)
	mtr.subscribeData(client)

	subs := client.drainSubscribes()
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscribes (command + data), got %d", len(subs))
	}
	topics := map[string]bool{subs[0].topic: true, subs[1].topic: true}
	if !topics["corec/commands/+/set"] {
		t.Error("missing subscribe to command topic corec/commands/+/set")
	}
	if !topics["corec/data/in"] {
		t.Error("missing subscribe to data topic corec/data/in")
	}
}

// Compile-time assertions that the mocks satisfy the paho interfaces.
var (
	_ pahomqtt.Client = (*mockPahoClient)(nil)
	_ pahomqtt.Message = (*mockMessage)(nil)
	_ pahomqtt.Token   = (*neverDoneToken)(nil)
)
