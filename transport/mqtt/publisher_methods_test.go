package mqtt

import (
	"context"
	"strings"
	"testing"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/CoreC-Dev/CoreC/core"
)

// These tests exercise metadata methods, channel plumbing, lifecycle error
// handling, and config-parsing edge cases for the MQTT transport.  None of
// them require a running MQTT broker, so they must never be skipped.

// newInitdTransport is a helper that builds, configures, and Inits an
// MQTTTransport from the given settings, failing the test on any error.
func newInitdTransport(t *testing.T, name string, settings map[string]any) *MQTTTransport {
	t.Helper()
	cfg := core.TransportConfig{
		Name:     name,
		Type:     "mqtt",
		Settings: settings,
	}
	tr, err := NewMQTTTransport(cfg)
	if err != nil {
		t.Fatalf("NewMQTTTransport(%q) failed: %v", name, err)
	}
	mtr := tr.(*MQTTTransport)
	if err := mtr.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init(%q) failed: %v", name, err)
	}
	return mtr
}

// ─── Init error paths ───────────────────────────────────────────────

// TestMQTTInitErrorPaths verifies that Init rejects malformed configuration
// with descriptive errors rather than panicking or silently succeeding.
func TestMQTTInitErrorPaths(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		wantErr  string // expected substring of the error message
	}{
		{
			name:     "missing broker key",
			settings: map[string]any{"client-id": "x"},
			wantErr:  "broker is required",
		},
		{
			name:     "broker wrong type (int not string)",
			settings: map[string]any{"broker": 1883},
			wantErr:  "broker is required",
		},
		{
			name:     "broker wrong type (nil)",
			settings: map[string]any{"broker": nil},
			wantErr:  "broker is required",
		},
		{
			name:     "invalid topic template (unclosed action)",
			settings: map[string]any{"broker": "tcp://x:1883", "topic-template": "{{.Driver"},
			wantErr:  "invalid topic template",
		},
		{
			name: "invalid parser config with data-topic",
			settings: map[string]any{
				"broker":     "tcp://x:1883",
				"data-topic": "data/in",
				"parser":     map[string]any{"type": "no-such-parser"},
			},
			wantErr: "invalid parser config",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := core.TransportConfig{
				Name:     "err-mqtt",
				Type:     "mqtt",
				Settings: tc.settings,
			}
			tr, err := NewMQTTTransport(cfg)
			if err != nil {
				t.Fatalf("NewMQTTTransport failed: %v", err)
			}
			mtr := tr.(*MQTTTransport)

			err = mtr.Init(context.Background(), cfg)
			if err == nil {
				t.Fatalf("expected Init to fail with %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

// ─── Config parsing edge cases ──────────────────────────────────────

// TestMQTTConfigParsingEdgeCases verifies that YAML-style values (float64
// numbers, string durations, bool flags, credentials) are parsed into the
// correct typed fields. This mirrors what a real YAML loader would produce.
func TestMQTTConfigParsingEdgeCases(t *testing.T) {
	settings := map[string]any{
		"broker":                 "tcp://broker.local:1883",
		"qos":                    float64(2), // YAML unquoted numbers decode to float64
		"retained":               true,
		"username":               "user1",
		"password":               "pass1",
		"keep-alive":             "30s",
		"connect-timeout":        "15s",
		"auto-reconnect":         false,
		"clean-session":          false,
		"connect-retry":          false,
		"connect-retry-interval": "2s",
		"subscribe-timeout":      "3s",
		"publish-timeout":        "4s",
		"disconnect-quiesce":     "500ms",
		"command-topic":          "cmd/+/set",
		"data-topic":             "data/in",
		// client-id intentionally omitted to exercise auto-generation.
	}
	mtr := newInitdTransport(t, "edge-mqtt", settings)

	// Broker
	if mtr.broker != "tcp://broker.local:1883" {
		t.Errorf("broker: got %q, want tcp://broker.local:1883", mtr.broker)
	}
	// Auto-generated client-id must be prefixed with the project name and the
	// configured transport name so instances are distinguishable on the broker.
	if !strings.HasPrefix(mtr.clientID, projectName+"-edge-mqtt-") {
		t.Errorf("client-id: got %q, want prefix %q", mtr.clientID, projectName+"-edge-mqtt-")
	}
	// QoS parsed from float64 (YAML) and truncated to a byte.
	if mtr.qos != 2 {
		t.Errorf("qos: got %d, want 2", mtr.qos)
	}
	// Retained flag
	if !mtr.retained {
		t.Error("retained: got false, want true")
	}
	// Credentials
	if mtr.username != "user1" {
		t.Errorf("username: got %q, want user1", mtr.username)
	}
	if mtr.password != "pass1" {
		t.Errorf("password: got %q, want pass1", mtr.password)
	}
	// Parsed durations
	if mtr.keepAlive != 30*time.Second {
		t.Errorf("keepAlive: got %v, want 30s", mtr.keepAlive)
	}
	if mtr.connectTimeout != 15*time.Second {
		t.Errorf("connectTimeout: got %v, want 15s", mtr.connectTimeout)
	}
	if mtr.connectRetryInterval != 2*time.Second {
		t.Errorf("connectRetryInterval: got %v, want 2s", mtr.connectRetryInterval)
	}
	if mtr.subscribeTimeout != 3*time.Second {
		t.Errorf("subscribeTimeout: got %v, want 3s", mtr.subscribeTimeout)
	}
	if mtr.publishTimeout != 4*time.Second {
		t.Errorf("publishTimeout: got %v, want 4s", mtr.publishTimeout)
	}
	if mtr.disconnectQuiesce != 500*time.Millisecond {
		t.Errorf("disconnectQuiesce: got %v, want 500ms", mtr.disconnectQuiesce)
	}
	// Bool flags overridden to false
	if mtr.autoReconnect {
		t.Error("autoReconnect: got true, want false")
	}
	if mtr.cleanSession {
		t.Error("cleanSession: got true, want false")
	}
	if mtr.connectRetry {
		t.Error("connectRetry: got true, want false")
	}
	// Topics
	if mtr.commandTopic != "cmd/+/set" {
		t.Errorf("commandTopic: got %q, want cmd/+/set", mtr.commandTopic)
	}
	if mtr.dataTopic != "data/in" {
		t.Errorf("dataTopic: got %q, want data/in", mtr.dataTopic)
	}
	if mtr.dataParser == nil {
		t.Error("dataParser: got nil, want a non-nil parser for data-topic")
	}
}

// TestMQTTConfigDefaults verifies that omitted settings fall back to the
// documented defaults (QoS 1, 60s keep-alive, true for reconnect flags,
// default topic template, etc.).
func TestMQTTConfigDefaults(t *testing.T) {
	mtr := newInitdTransport(t, "defaults-mqtt", map[string]any{
		"broker": "tcp://x:1883",
	})

	if mtr.qos != 1 {
		t.Errorf("default qos: got %d, want 1", mtr.qos)
	}
	if mtr.retained {
		t.Error("default retained: got true, want false")
	}
	if mtr.keepAlive != 60*time.Second {
		t.Errorf("default keepAlive: got %v, want 60s", mtr.keepAlive)
	}
	if mtr.connectTimeout != 10*time.Second {
		t.Errorf("default connectTimeout: got %v, want 10s", mtr.connectTimeout)
	}
	if mtr.connectRetryInterval != 5*time.Second {
		t.Errorf("default connectRetryInterval: got %v, want 5s", mtr.connectRetryInterval)
	}
	if mtr.subscribeTimeout != 5*time.Second {
		t.Errorf("default subscribeTimeout: got %v, want 5s", mtr.subscribeTimeout)
	}
	if mtr.publishTimeout != 5*time.Second {
		t.Errorf("default publishTimeout: got %v, want 5s", mtr.publishTimeout)
	}
	if mtr.disconnectQuiesce != time.Second {
		t.Errorf("default disconnectQuiesce: got %v, want 1s", mtr.disconnectQuiesce)
	}
	if !mtr.autoReconnect {
		t.Error("default autoReconnect: got false, want true")
	}
	if !mtr.cleanSession {
		t.Error("default cleanSession: got false, want true")
	}
	if !mtr.connectRetry {
		t.Error("default connectRetry: got false, want true")
	}
	if mtr.topicTemplate == nil {
		t.Error("default topicTemplate: got nil, want the default corec/{{.Driver}}/{{.Tag}} template")
	}
	// The default template should render like the existing TestMQTTDefaultTopicTemplate.
	topic, err := mtr.buildTopic(core.DataPoint{Driver: "opc", Tag: "temp"})
	if err != nil {
		t.Fatalf("buildTopic with default template failed: %v", err)
	}
	if want := projectName + "/opc/temp"; topic != want {
		t.Errorf("default topic render: got %q, want %q", topic, want)
	}
}

// TestMQTTConfigExplicitClientID verifies a provided client-id is used verbatim
// (no auto-generation prefix).
func TestMQTTConfigExplicitClientID(t *testing.T) {
	mtr := newInitdTransport(t, "cid-mqtt", map[string]any{
		"broker":    "tcp://x:1883",
		"client-id": "fixed-client-42",
	})
	if mtr.clientID != "fixed-client-42" {
		t.Errorf("clientID: got %q, want fixed-client-42", mtr.clientID)
	}
}

// ─── Metadata: Name / Type / Status ─────────────────────────────────

// TestMQTTMetadataAndStatus verifies the metadata accessors and the Status
// snapshot immediately after Init (no Start): the transport is disconnected,
// all counters are zero, and no publish has occurred.
func TestMQTTMetadataAndStatus(t *testing.T) {
	mtr := newInitdTransport(t, "meta-mqtt", map[string]any{"broker": "tcp://x:1883"})

	if got := mtr.Name(); got != "meta-mqtt" {
		t.Errorf("Name(): got %q, want meta-mqtt", got)
	}
	if got := mtr.Type(); got != "mqtt" {
		t.Errorf("Type(): got %q, want mqtt", got)
	}

	st := mtr.Status()
	if st.Name != "meta-mqtt" {
		t.Errorf("Status.Name: got %q, want meta-mqtt", st.Name)
	}
	if st.Type != "mqtt" {
		t.Errorf("Status.Type: got %q, want mqtt", st.Type)
	}
	if st.State != core.StateDisconnected {
		t.Errorf("Status.State: got %v, want StateDisconnected", st.State)
	}
	if st.Published != 0 {
		t.Errorf("Status.Published: got %d, want 0", st.Published)
	}
	if st.Failed != 0 {
		t.Errorf("Status.Failed: got %d, want 0", st.Failed)
	}
	if st.Received != 0 {
		t.Errorf("Status.Received: got %d, want 0", st.Received)
	}
	if !st.LastPublish.IsZero() {
		t.Errorf("Status.LastPublish: got %v, want zero time", st.LastPublish)
	}
	// No client has been created (Start not called), so QueueSize is reported
	// as 0 regardless of any buffered commands.
	if st.QueueSize != 0 {
		t.Errorf("Status.QueueSize: got %d, want 0 (client is nil)", st.QueueSize)
	}
}

// TestMQTTStatusQueueSize verifies the QueueSize reporting logic: it stays 0
// while no client is attached, and reflects len(commandCh) once a client
// exists. The client is constructed (white-box) but never connected, so no
// broker is required.
func TestMQTTStatusQueueSize(t *testing.T) {
	mtr := newInitdTransport(t, "q-mqtt", map[string]any{"broker": "tcp://x:1883"})

	// Queue a command. With no client, Status must still report 0.
	mtr.commandCh <- core.WriteCommand{Driver: "d", Tag: "t"}
	if st := mtr.Status(); st.QueueSize != 0 {
		t.Errorf("QueueSize with nil client: got %d, want 0", st.QueueSize)
	}

	// Attach a non-connected paho client to exercise the len(commandCh) branch.
	opts := pahomqtt.NewClientOptions()
	opts.AddBroker("tcp://x:1883")
	mtr.client = pahomqtt.NewClient(opts)

	if st := mtr.Status(); st.QueueSize != 1 {
		t.Errorf("QueueSize with client set: got %d, want 1", st.QueueSize)
	}
}

// ─── OnData ─────────────────────────────────────────────────────────

// TestMQTTOnDataChannel verifies OnData returns nil when no data-topic is
// configured, and a usable non-nil channel (with round-trip and identity)
// when a data-topic is configured.
func TestMQTTOnDataChannel(t *testing.T) {
	t.Run("nil without data-topic", func(t *testing.T) {
		mtr := newInitdTransport(t, "nodata", map[string]any{"broker": "tcp://x:1883"})
		if ch := mtr.OnData(); ch != nil {
			t.Errorf("OnData without data-topic: got non-nil channel %v, want nil", ch)
		}
	})

	t.Run("non-nil with round-trip and identity", func(t *testing.T) {
		mtr := newInitdTransport(t, "withdata", map[string]any{
			"broker":     "tcp://x:1883",
			"data-topic": "data/in",
		})
		ch := mtr.OnData()
		if ch == nil {
			t.Fatal("OnData with data-topic: got nil, want non-nil channel")
		}

		// Round-trip a DataPoint through the channel exactly as a subscription
		// handler would, and confirm the consumer reads the same value.
		want := core.DataPoint{
			Driver:    "upstream",
			Tag:       "temp",
			Value:     42.5,
			Type:      core.TypeFloat64,
			Timestamp: time.Now(),
		}
		mtr.dataCh <- want
		got := <-ch
		if got.Driver != want.Driver || got.Tag != want.Tag || got.Value != want.Value {
			t.Errorf("OnData round-trip: got %+v, want %+v", got, want)
		}

		// Repeated calls must return the same underlying channel.
		if mtr.OnData() != ch {
			t.Error("OnData returned a different channel on repeated call")
		}
	})
}

// ─── OnCommand (thorough) ───────────────────────────────────────────

// TestMQTTOnCommandChannelThorough verifies the command channel is non-nil,
// buffered to the configured capacity, supports a full round-trip, and is
// stable across repeated calls.
func TestMQTTOnCommandChannelThorough(t *testing.T) {
	mtr := newInitdTransport(t, "cmd-mqtt", map[string]any{
		"broker":        "tcp://x:1883",
		"command-topic": "cmd/+/set",
	})

	ch := mtr.OnCommand()
	if ch == nil {
		t.Fatal("OnCommand: got nil, want non-nil channel")
	}

	// Round-trip a WriteCommand as the subscription handler would deliver it.
	// Send via the bidirectional commandCh (white-box) and read back through
	// the receive-only OnCommand() accessor to prove they are the same channel.
	want := core.WriteCommand{
		Driver: "opc",
		Device: "plc1",
		Tag:    "setpoint",
		Value:  100.0,
		Type:   core.TypeFloat64,
	}
	mtr.commandCh <- want
	got := <-ch
	if got != want {
		t.Errorf("OnCommand round-trip: got %+v, want %+v", got, want)
	}

	// Identity: repeated calls return the same channel.
	if mtr.OnCommand() != ch {
		t.Error("OnCommand returned a different channel on repeated call")
	}

	// Capacity matches the default buffer size (BufferSize unset → default).
	if got, want := cap(ch), core.DefaultCommandBufferSize; got != want {
		t.Errorf("command channel capacity: got %d, want %d", got, want)
	}

	// The channel must accept up to its capacity without blocking, then reject
	// the next non-blocking send (verifying it is genuinely bounded).
	for i := 0; i < core.DefaultCommandBufferSize; i++ {
		select {
		case mtr.commandCh <- core.WriteCommand{Tag: "t"}:
		default:
			t.Fatalf("channel unexpectedly full at index %d (capacity %d)", i, core.DefaultCommandBufferSize)
		}
	}
	select {
	case mtr.commandCh <- core.WriteCommand{Tag: "overflow"}:
		t.Error("expected the bounded channel to be full, but an extra send succeeded")
	default:
		// expected: channel is at capacity
	}
}

// ─── Stop ───────────────────────────────────────────────────────────

// TestMQTTStopNotConnected verifies that Stop is safe to call on a transport
// that was never started (no client, no cancel), returns no error, leaves the
// state disconnected, and is idempotent.
func TestMQTTStopNotConnected(t *testing.T) {
	t.Run("fresh transport no Init", func(t *testing.T) {
		tr, err := NewMQTTTransport(core.TransportConfig{Name: "stop-fresh", Type: "mqtt"})
		if err != nil {
			t.Fatalf("NewMQTTTransport failed: %v", err)
		}
		mtr := tr.(*MQTTTransport)

		if err := mtr.Stop(); err != nil {
			t.Errorf("Stop on fresh transport: got error %v, want nil", err)
		}
		if st := mtr.Status(); st.State != core.StateDisconnected {
			t.Errorf("state after Stop: got %v, want StateDisconnected", st.State)
		}
	})

	t.Run("after Init no Start", func(t *testing.T) {
		mtr := newInitdTransport(t, "stop-init", map[string]any{"broker": "tcp://x:1883"})

		if err := mtr.Stop(); err != nil {
			t.Errorf("Stop after Init: got error %v, want nil", err)
		}
		if st := mtr.Status(); st.State != core.StateDisconnected {
			t.Errorf("state after Stop: got %v, want StateDisconnected", st.State)
		}

		// Idempotent: a second Stop must not panic or error.
		if err := mtr.Stop(); err != nil {
			t.Errorf("second Stop: got error %v, want nil", err)
		}
	})
}

// ─── Publish / PublishBatch (not connected) ─────────────────────────

// TestMQTTPublishNotConnected verifies that Publish on a transport with no
// connected client returns a descriptive error (mentioning the transport
// name) and increments the failed counter, rather than panicking.
func TestMQTTPublishNotConnected(t *testing.T) {
	mtr := newInitdTransport(t, "pub-mqtt", map[string]any{"broker": "tcp://x:1883"})

	point := core.DataPoint{
		Driver:    "d",
		Tag:       "t",
		Value:     1.0,
		Type:      core.TypeFloat64,
		Timestamp: time.Now(),
	}
	err := mtr.Publish(context.Background(), point)
	if err == nil {
		t.Fatal("Publish when not connected: got nil error, want a non-nil error")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("Publish error: got %q, want it to contain 'not connected'", err.Error())
	}
	if !strings.Contains(err.Error(), "pub-mqtt") {
		t.Errorf("Publish error: got %q, want it to contain the transport name 'pub-mqtt'", err.Error())
	}
	if st := mtr.Status(); st.Failed != 1 {
		t.Errorf("Failed counter after one failed Publish: got %d, want 1", st.Failed)
	}
	if st := mtr.Status(); st.Published != 0 {
		t.Errorf("Published counter after failed Publish: got %d, want 0", st.Published)
	}
}

// TestMQTTPublishBatchNotConnected verifies PublishBatch behaviour when the
// transport is not connected: an empty batch is a no-op (nil error, no
// failures), while a non-empty batch returns the first error but still
// attempts every point so the failed counter reflects all of them.
func TestMQTTPublishBatchNotConnected(t *testing.T) {
	t.Run("empty batch is a no-op", func(t *testing.T) {
		mtr := newInitdTransport(t, "batch-empty", map[string]any{"broker": "tcp://x:1883"})

		if err := mtr.PublishBatch(context.Background(), nil); err != nil {
			t.Errorf("PublishBatch(nil): got error %v, want nil", err)
		}
		if st := mtr.Status(); st.Failed != 0 {
			t.Errorf("Failed after empty batch: got %d, want 0", st.Failed)
		}
	})

	t.Run("non-empty batch attempts all and returns first error", func(t *testing.T) {
		mtr := newInitdTransport(t, "batch-full", map[string]any{"broker": "tcp://x:1883"})

		points := []core.DataPoint{
			{Driver: "d", Tag: "t1", Value: 1.0, Timestamp: time.Now()},
			{Driver: "d", Tag: "t2", Value: 2.0, Timestamp: time.Now()},
			{Driver: "d", Tag: "t3", Value: 3.0, Timestamp: time.Now()},
		}
		err := mtr.PublishBatch(context.Background(), points)
		if err == nil {
			t.Fatal("PublishBatch when not connected: got nil error, want a non-nil error")
		}
		if !strings.Contains(err.Error(), "not connected") {
			t.Errorf("PublishBatch error: got %q, want it to contain 'not connected'", err.Error())
		}
		// Every point must be attempted, so the failed counter must equal the
		// batch size — this proves PublishBatch does not bail out on the first
		// failure.
		if st := mtr.Status(); st.Failed != uint64(len(points)) {
			t.Errorf("Failed counter: got %d, want %d (all points attempted)", st.Failed, len(points))
		}
	})
}
