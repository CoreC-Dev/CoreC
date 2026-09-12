// Package mqtt implements the MQTT transport for CoreC.
package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/transport/parser"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// projectName is the prefix used in default MQTT topic templates and
// client IDs. Change here if the project is rebranded.
const projectName = "corec"

// MQTTTransport implements core.Transport for MQTT protocol.
type MQTTTransport struct {
	mu sync.RWMutex

	name   string
	config core.TransportConfig

	// MQTT client
	client pahomqtt.Client

	// MQTT settings
	broker        string
	clientID      string
	username      string
	password      string
	qos           byte
	retained      bool
	topicTemplate *template.Template
	commandTopic  string
	dataTopic     string // topic to subscribe for incoming data (chained-core inbound)
	dataParser    parser.Parser

	// Connection options
	keepAlive            time.Duration
	connectTimeout       time.Duration
	autoReconnect        bool
	cleanSession         bool
	connectRetry         bool
	connectRetryInterval time.Duration

	// Operation timeouts
	subscribeTimeout  time.Duration
	publishTimeout    time.Duration
	disconnectQuiesce time.Duration // passed to paho as milliseconds

	// State
	state core.ConnState

	// Counters
	published   atomic.Uint64
	failed      atomic.Uint64
	received    atomic.Uint64
	lastPublish time.Time

	// Command channel (for receiving write commands via MQTT subscriptions)
	commandCh chan core.WriteCommand

	// Data channel (for receiving data points via MQTT subscriptions — chained core)
	dataCh chan core.DataPoint

	ctx    context.Context
	cancel context.CancelFunc
}

// NewMQTTTransport creates a new MQTT transport from configuration.
func NewMQTTTransport(config core.TransportConfig) (core.Transport, error) {
	bufSize := config.BufferSize
	if bufSize <= 0 {
		bufSize = core.DefaultCommandBufferSize
	}
	t := &MQTTTransport{
		name:      config.Name,
		config:    config,
		state:     core.StateDisconnected,
		commandCh: make(chan core.WriteCommand, bufSize),
		dataCh:    make(chan core.DataPoint, bufSize),
	}
	return t, nil
}

func (t *MQTTTransport) Init(ctx context.Context, config core.TransportConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	settings := config.Settings

	// Parse broker
	if broker, ok := settings["broker"].(string); ok {
		t.broker = broker
	} else {
		return fmt.Errorf("mqtt: broker is required")
	}

	// Parse client ID
	if clientID, ok := settings["client-id"].(string); ok {
		t.clientID = clientID
	} else {
		t.clientID = fmt.Sprintf("%s-%s-%d", projectName, config.Name, time.Now().UnixMilli())
	}

	// Parse credentials
	if username, ok := settings["username"].(string); ok {
		t.username = username
	}
	if password, ok := settings["password"].(string); ok {
		t.password = password
	}

	// Parse QoS
	t.qos = byte(util.GetIntSetting(settings, "qos", 1))

	// Parse retained
	if retained, ok := settings["retained"].(bool); ok {
		t.retained = retained
	}

	// Connection settings
	t.keepAlive = util.GetDurationSetting(settings, "keep-alive", 60*time.Second)
	t.connectTimeout = util.GetDurationSetting(settings, "connect-timeout", 10*time.Second)
	t.autoReconnect = util.GetBoolSetting(settings, "auto-reconnect", true)
	t.cleanSession = util.GetBoolSetting(settings, "clean-session", true)
	t.connectRetry = util.GetBoolSetting(settings, "connect-retry", true)
	t.connectRetryInterval = util.GetDurationSetting(settings, "connect-retry-interval", 5*time.Second)

	// Operation timeouts
	t.subscribeTimeout = util.GetDurationSetting(settings, "subscribe-timeout", 5*time.Second)
	t.publishTimeout = util.GetDurationSetting(settings, "publish-timeout", 5*time.Second)
	t.disconnectQuiesce = util.GetDurationSetting(settings, "disconnect-quiesce", time.Second)

	// Parse topic template
	if topicTpl, ok := settings["topic-template"].(string); ok {
		tmpl, err := template.New("topic").Parse(topicTpl)
		if err != nil {
			return fmt.Errorf("mqtt: invalid topic template: %w", err)
		}
		t.topicTemplate = tmpl
	} else {
		tmpl, _ := template.New("topic").Parse(projectName + "/{{.Driver}}/{{.Tag}}")
		t.topicTemplate = tmpl
	}

	// Parse command topic (for receiving write commands)
	if cmdTopic, ok := settings["command-topic"].(string); ok {
		t.commandTopic = cmdTopic
	}

	// Parse data topic (for receiving data points — chained core inbound)
	if dataTopic, ok := settings["data-topic"].(string); ok && dataTopic != "" {
		t.dataTopic = dataTopic
		p, err := parser.New(settings)
		if err != nil {
			return fmt.Errorf("mqtt: invalid parser config: %w", err)
		}
		t.dataParser = p
	}

	slog.Info("mqtt transport initialized",
		"name", t.name,
		"broker", t.broker,
		"client-id", t.clientID,
	)

	return nil
}

func (t *MQTTTransport) Start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(ctx)

	// Build MQTT client options
	opts := pahomqtt.NewClientOptions()
	opts.AddBroker(t.broker)
	opts.SetClientID(t.clientID)
	opts.SetKeepAlive(t.keepAlive)
	opts.SetConnectTimeout(t.connectTimeout)
	opts.SetAutoReconnect(t.autoReconnect)
	opts.SetCleanSession(t.cleanSession)
	opts.SetConnectRetry(t.connectRetry)
	opts.SetConnectRetryInterval(t.connectRetryInterval)

	if t.username != "" {
		opts.SetUsername(t.username)
	}
	if t.password != "" {
		opts.SetPassword(t.password)
	}

	// Connection handlers
	opts.SetOnConnectHandler(func(c pahomqtt.Client) {
		slog.Info("mqtt connected", "name", t.name, "broker", t.broker)
		t.mu.Lock()
		t.state = core.StateConnected
		t.mu.Unlock()

		// Subscribe to command topic if configured
		if t.commandTopic != "" {
			t.subscribeCommands(c)
		}

		// Subscribe to data topic if configured (chained core inbound)
		if t.dataTopic != "" {
			t.subscribeData(c)
		}
	})

	opts.SetConnectionLostHandler(func(c pahomqtt.Client, err error) {
		slog.Warn("mqtt connection lost", "name", t.name, "error", err)
		t.mu.Lock()
		t.state = core.StateConnecting // auto-reconnect is enabled
		t.mu.Unlock()
	})

	opts.SetReconnectingHandler(func(c pahomqtt.Client, opts *pahomqtt.ClientOptions) {
		slog.Info("mqtt reconnecting", "name", t.name)
	})

	// Create and connect
	t.client = pahomqtt.NewClient(opts)

	t.mu.Lock()
	t.state = core.StateConnecting
	t.mu.Unlock()

	token := t.client.Connect()
	if ok := token.WaitTimeout(t.connectTimeout); !ok {
		slog.Warn("mqtt connect timed out, will retry in background",
			"name", t.name, "broker", t.broker)
		// Don't fail — ConnectRetry will handle it
		return nil
	}

	if err := token.Error(); err != nil {
		slog.Warn("mqtt connect failed, will retry in background",
			"name", t.name, "error", err)
		// Don't fail — ConnectRetry will handle it
		return nil
	}

	slog.Info("mqtt transport started", "name", t.name, "broker", t.broker)
	return nil
}

func (t *MQTTTransport) subscribeCommands(c pahomqtt.Client) {
	token := c.Subscribe(t.commandTopic, t.qos, func(client pahomqtt.Client, msg pahomqtt.Message) {
		slog.Debug("mqtt command received",
			"topic", msg.Topic(),
			"payload_size", len(msg.Payload()),
		)

		var cmd core.WriteCommand
		if err := json.Unmarshal(msg.Payload(), &cmd); err != nil {
			slog.Error("mqtt: failed to parse command",
				"topic", msg.Topic(),
				"error", err,
			)
			return
		}

		select {
		case t.commandCh <- cmd:
		default:
			slog.Warn("mqtt command channel full, dropping command",
				"driver", cmd.Driver, "tag", cmd.Tag)
		}
	})

	if !token.WaitTimeout(t.subscribeTimeout) {
		slog.Error("mqtt command subscribe timed out",
			"topic", t.commandTopic, "timeout", t.subscribeTimeout)
	} else if err := token.Error(); err != nil {
		slog.Error("mqtt command subscribe failed",
			"topic", t.commandTopic, "error", err)
	} else {
		slog.Info("mqtt subscribed to command topic",
			"name", t.name, "topic", t.commandTopic)
	}
}

// subscribeData subscribes to the data topic and feeds parsed DataPoints
// into the dataCh channel.  This is the chained-core inbound path
// for MQTT.
func (t *MQTTTransport) subscribeData(c pahomqtt.Client) {
	token := c.Subscribe(t.dataTopic, t.qos, func(client pahomqtt.Client, msg pahomqtt.Message) {
		slog.Debug("mqtt data received",
			"topic", msg.Topic(),
			"payload_size", len(msg.Payload()),
		)

		point, err := t.dataParser.Parse(msg.Payload(), msg.Topic())
		if err != nil {
			slog.Error("mqtt: failed to parse data point",
				"topic", msg.Topic(),
				"error", err,
			)
			return
		}

		select {
		case t.dataCh <- point:
			t.received.Add(1)
		default:
			slog.Warn("mqtt data channel full, dropping data point",
				"driver", point.Driver, "tag", point.Tag)
		}
	})

	if !token.WaitTimeout(t.subscribeTimeout) {
		slog.Error("mqtt data subscribe timed out",
			"topic", t.dataTopic, "timeout", t.subscribeTimeout)
	} else if err := token.Error(); err != nil {
		slog.Error("mqtt data subscribe failed",
			"topic", t.dataTopic, "error", err)
	} else {
		slog.Info("mqtt subscribed to data topic",
			"name", t.name, "topic", t.dataTopic)
	}
}

func (t *MQTTTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}

	if t.client != nil && t.client.IsConnected() {
		// Unsubscribe from command topic
		if t.commandTopic != "" {
			t.client.Unsubscribe(t.commandTopic)
		}
		// Unsubscribe from data topic
		if t.dataTopic != "" {
			t.client.Unsubscribe(t.dataTopic)
		}
		// Disconnect with configurable quiesce (paho expects milliseconds)
		t.client.Disconnect(uint(t.disconnectQuiesce / time.Millisecond))
	}

	t.mu.Lock()
	t.state = core.StateDisconnected
	t.mu.Unlock()

	// Don't close commandCh here — engine may still be reading
	slog.Info("mqtt transport stopped", "name", t.name)
	return nil
}

func (t *MQTTTransport) Publish(ctx context.Context, point core.DataPoint) error {
	if t.client == nil || !t.client.IsConnected() {
		t.failed.Add(1)
		return fmt.Errorf("mqtt transport %s is not connected", t.name)
	}

	// Build topic from template
	topic, err := t.buildTopic(point)
	if err != nil {
		t.failed.Add(1)
		return fmt.Errorf("failed to build topic: %w", err)
	}

	// Serialize payload
	payload, err := json.Marshal(point)
	if err != nil {
		t.failed.Add(1)
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Publish with timeout
	token := t.client.Publish(topic, t.qos, t.retained, payload)
	if ok := token.WaitTimeout(t.publishTimeout); !ok {
		t.failed.Add(1)
		return fmt.Errorf("mqtt publish timed out for topic %s", topic)
	}

	if err := token.Error(); err != nil {
		t.failed.Add(1)
		return fmt.Errorf("mqtt publish failed: %w", err)
	}

	t.published.Add(1)
	t.mu.Lock()
	t.lastPublish = time.Now()
	t.mu.Unlock()

	slog.Debug("mqtt published",
		"topic", topic,
		"driver", point.Driver,
		"tag", point.Tag,
		"value", point.Value,
	)

	return nil
}

func (t *MQTTTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	var firstErr error
	for i := range points {
		if err := t.Publish(ctx, points[i]); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// Continue publishing remaining points
		}
	}
	return firstErr
}

func (t *MQTTTransport) OnCommand() <-chan core.WriteCommand {
	return t.commandCh
}

// OnData returns the channel of data points received from the subscribed
// data topic.  Returns a non-nil channel only when data-topic is configured.
func (t *MQTTTransport) OnData() <-chan core.DataPoint {
	if t.dataTopic == "" {
		return nil
	}
	return t.dataCh
}

func (t *MQTTTransport) Name() string { return t.name }
func (t *MQTTTransport) Type() string { return "mqtt" }

func (t *MQTTTransport) Status() core.TransportStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	queueSize := 0
	if t.client != nil {
		// No direct queue size API, but we track pending commands
		queueSize = len(t.commandCh)
	}

	return core.TransportStatus{
		Name:        t.name,
		Type:        "mqtt",
		State:       t.state,
		Published:   t.published.Load(),
		Failed:      t.failed.Load(),
		Received:    t.received.Load(),
		LastPublish: t.lastPublish,
		QueueSize:   queueSize,
	}
}

// ============================================================
// Helper functions
// ============================================================

func (t *MQTTTransport) buildTopic(point core.DataPoint) (string, error) {
	var buf bytes.Buffer

	data := map[string]string{
		"Driver": point.Driver,
		"Device": point.Device,
		"Group":  point.Group,
		"Tag":    point.Tag,
	}

	if err := t.topicTemplate.Execute(&buf, data); err != nil {
		return "", err
	}

	// Clean up empty segments
	topic := buf.String()
	for strings.Contains(topic, "//") {
		topic = strings.ReplaceAll(topic, "//", "/")
	}

	return topic, nil
}
