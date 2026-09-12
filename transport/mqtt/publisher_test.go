package mqtt

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// These tests exercise pure logic (topic templating and config parsing) and
// do NOT require a running MQTT broker, so they must not be skipped.

func TestMQTTTopicTemplating(t *testing.T) {
	transportCfg := core.TransportConfig{
		Name: "test-mqtt",
		Type: "mqtt",
		Settings: map[string]any{
			"broker":         "tcp://127.0.0.1:1883",
			"topic-template": "devices/{{.Driver}}/{{.Group}}/{{.Tag}}",
		},
	}

	tr, err := NewMQTTTransport(transportCfg)
	if err != nil {
		t.Fatalf("NewMQTTTransport failed: %v", err)
	}

	mqttTr := tr.(*MQTTTransport)
	if err := mqttTr.Init(context.Background(), transportCfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	point := core.DataPoint{
		Driver:    "line1",
		Device:    "plc01",
		Group:     "sensors",
		Tag:       "pressure",
		Value:     12.34,
		Type:      core.TypeFloat32,
		Timestamp: time.Now(),
	}

	topic, err := mqttTr.buildTopic(point)
	if err != nil {
		t.Fatalf("buildTopic failed: %v", err)
	}

	expected := "devices/line1/sensors/pressure"
	if topic != expected {
		t.Errorf("expected topic %q, got %q", expected, topic)
	}
}

// TestMQTTDefaultTopicTemplate verifies that when no topic-template is
// configured, the default corec/{{.Driver}}/{{.Tag}} is used.
func TestMQTTDefaultTopicTemplate(t *testing.T) {
	transportCfg := core.TransportConfig{
		Name: "test-mqtt-default",
		Type: "mqtt",
		Settings: map[string]any{
			"broker": "tcp://127.0.0.1:1883",
		},
	}

	tr, err := NewMQTTTransport(transportCfg)
	if err != nil {
		t.Fatalf("NewMQTTTransport failed: %v", err)
	}
	mqttTr := tr.(*MQTTTransport)
	if err := mqttTr.Init(context.Background(), transportCfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	point := core.DataPoint{
		Driver: "opc-plc",
		Tag:    "temperature",
	}
	topic, err := mqttTr.buildTopic(point)
	if err != nil {
		t.Fatalf("buildTopic failed: %v", err)
	}
	expected := "corec/opc-plc/temperature"
	if topic != expected {
		t.Errorf("expected default topic %q, got %q", expected, topic)
	}
}

// TestMQTTTopicEmptyGroupCleanup verifies that empty Group segments are
// collapsed (the "//" cleanup) rather than producing a malformed topic.
func TestMQTTTopicEmptyGroupCleanup(t *testing.T) {
	transportCfg := core.TransportConfig{
		Name: "test-mqtt-group",
		Type: "mqtt",
		Settings: map[string]any{
			"broker":         "tcp://127.0.0.1:1883",
			"topic-template": "devices/{{.Driver}}/{{.Group}}/{{.Tag}}",
		},
	}

	tr, err := NewMQTTTransport(transportCfg)
	if err != nil {
		t.Fatalf("NewMQTTTransport failed: %v", err)
	}
	mqttTr := tr.(*MQTTTransport)
	if err := mqttTr.Init(context.Background(), transportCfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Group is empty — the template produces "devices/line1//pressure"
	// which buildTopic must collapse to "devices/line1/pressure".
	point := core.DataPoint{
		Driver: "line1",
		Tag:    "pressure",
	}
	topic, err := mqttTr.buildTopic(point)
	if err != nil {
		t.Fatalf("buildTopic failed: %v", err)
	}
	expected := "devices/line1/pressure"
	if topic != expected {
		t.Errorf("expected cleaned topic %q, got %q", expected, topic)
	}
}

func TestMQTTConfigParsing(t *testing.T) {
	transportCfg := core.TransportConfig{
		Name: "custom-mqtt",
		Type: "mqtt",
		Settings: map[string]any{
			"broker":        "tcp://test.mosquitto.org:1883",
			"client-id":     "custom-client-123",
			"qos":           2,
			"command-topic": "commands/+/set",
		},
	}

	tr, err := NewMQTTTransport(transportCfg)
	if err != nil {
		t.Fatalf("NewMQTTTransport failed: %v", err)
	}

	mqttTr := tr.(*MQTTTransport)
	if err := mqttTr.Init(context.Background(), transportCfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if mqttTr.broker != "tcp://test.mosquitto.org:1883" {
		t.Errorf("expected broker 'tcp://test.mosquitto.org:1883', got %s", mqttTr.broker)
	}
	if mqttTr.clientID != "custom-client-123" {
		t.Errorf("expected clientID 'custom-client-123', got %s", mqttTr.clientID)
	}
	if mqttTr.qos != 2 {
		t.Errorf("expected qos 2, got %d", mqttTr.qos)
	}
	if mqttTr.commandTopic != "commands/+/set" {
		t.Errorf("expected commandTopic 'commands/+/set', got %s", mqttTr.commandTopic)
	}
}

// TestMQTTOnCommandChannel verifies the command-forwarding plumbing: after
// Init, OnCommand() returns a non-nil, readable channel. (A full end-to-end
// command round-trip requires a broker and is covered by integration tests.)
func TestMQTTOnCommandChannel(t *testing.T) {
	transportCfg := core.TransportConfig{
		Name: "test-mqtt-cmd",
		Type: "mqtt",
		Settings: map[string]any{
			"broker":        "tcp://127.0.0.1:1883",
			"command-topic": "commands/+/set",
		},
	}

	tr, err := NewMQTTTransport(transportCfg)
	if err != nil {
		t.Fatalf("NewMQTTTransport failed: %v", err)
	}
	mqttTr := tr.(*MQTTTransport)
	if err := mqttTr.Init(context.Background(), transportCfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	ch := mqttTr.OnCommand()
	if ch == nil {
		t.Fatal("OnCommand returned nil channel; expected a non-nil command channel")
	}
}
