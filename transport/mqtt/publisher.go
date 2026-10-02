// Package mqtt implements the MQTT transport for CoreC.
package mqtt

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
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

// TypeName identifies this transport's protocol. It is returned by Type()
// and embedded in Status() so the transport kind is reported consistently
// from a single source of truth rather than a scattered string literal.
const TypeName = "mqtt"

// MQTTTransport implements core.Transport for MQTT protocol.
type MQTTTransport struct {
	mu sync.RWMutex

	name   string
	config core.TransportConfig

	// MQTT client
	client pahomqtt.Client

	// MQTT settings
	broker               string
	clientID             string
	username             string
	password             string
	qos                  byte
	retained             bool
	topicTemplate        *template.Template
	commandTopic         string        // MQTT topic to subscribe for write commands
	commandSecret        string        // HMAC-SHA256 secret for authenticating command messages; empty = no auth
	commandForwardTopic  string        // MQTT topic to publish forwarded commands (chained-core passthrough)
	commandForwardSecret string        // HMAC-SHA256 secret for signing forwarded commands; empty = unsigned
	commandMaxSkew       time.Duration // max allowed timestamp drift for replay protection (default 5m); 0 = 5m
	commandStrictReplay  bool          // reject commands without a timestamp when true
	dataTopic            string        // topic to subscribe for incoming data (chained-core inbound)
	dataParser           parser.Parser

	// TLS settings (mqtts://, tls://, ssl://, ...).  Empty file paths with
	// a plaintext broker = no TLS (backward compatible).  Built into
	// tlsConfig at Init time and applied to the paho client at Start time.
	tlsCertFile string      // client certificate PEM (enables mutual TLS / mTLS)
	tlsKeyFile  string      // client private key PEM (enables mutual TLS / mTLS)
	tlsCAFile   string      // CA certificate PEM for server verification
	tlsConfig   *tls.Config // nil when TLS is not required

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
	// droppedCommands counts write commands dropped because commandCh was
	// full at ingress. Exposed via Status() for observability.
	droppedCommands atomic.Uint64

	// Command channel (for receiving write commands via MQTT subscriptions)
	commandCh chan core.WriteCommand

	// commandDeadLetter holds commands that could not be enqueued because
	// commandCh was full. Bounded by commandDeadLetterMax; oldest entries
	// are evicted when full. This is a transport-local last-resort store —
	// the paho message callback MUST stay non-blocking (see
	// handleCommandMessage), so this store is a mutex-guarded slice, not a
	// blocking channel. Entries are inspectable for operator replay.
	commandDeadLetterMu  sync.Mutex
	commandDeadLetter    []core.DeadLetterEntry
	commandDeadLetterMax int

	// Data channel (for receiving data points via MQTT subscriptions — chained core)
	dataCh chan core.DataPoint

	ctx    context.Context
	cancel context.CancelFunc

	// replayCache stores recently-seen command message hashes to prevent
	// replay within the command-max-skew window. It is a bounded map with
	// time-based eviction so entries expire after the skew window passes.
	replayCache *replayWindow
}

// NewMQTTTransport creates a new MQTT transport from configuration.
func NewMQTTTransport(config core.TransportConfig) (core.Transport, error) {
	bufSize := config.BufferSize
	if bufSize <= 0 {
		bufSize = core.DefaultCommandBufferSize
	}
	t := &MQTTTransport{
		name:                 config.Name,
		config:               config,
		state:                core.StateDisconnected,
		commandCh:            make(chan core.WriteCommand, bufSize),
		dataCh:               make(chan core.DataPoint, bufSize),
		commandDeadLetterMax: core.DefaultDeadLetterMaxLen,
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
	t.username = util.GetStringSetting(settings, "username", "")
	t.password = util.GetStringSetting(settings, "password", "")

	// Parse QoS
	t.qos = byte(util.GetIntSetting(settings, "qos", 1))

	// Parse retained
	if retained, ok := settings["retained"].(bool); ok {
		t.retained = retained
	}

	// Connection settings
	t.keepAlive = util.GetDurationSetting(settings, "keep-alive", core.DefaultKeepAlive)
	t.connectTimeout = util.GetDurationSetting(settings, "connect-timeout", 10*time.Second)
	t.autoReconnect = util.GetBoolSetting(settings, "auto-reconnect", true)
	t.cleanSession = util.GetBoolSetting(settings, "clean-session", true)
	t.connectRetry = util.GetBoolSetting(settings, "connect-retry", true)
	// connect-retry-interval is paho's fixed reconnect interval, not the
	// generic transport I/O timeout, so it keeps its own literal rather than
	// reusing core.DefaultTransportTimeout.
	t.connectRetryInterval = util.GetDurationSetting(settings, "connect-retry-interval", 5*time.Second)

	// Apply ±20% jitter to the reconnect interval to prevent thundering-herd
	// when multiple MQTT transports reconnect simultaneously after a broker
	// outage. Each instance gets a slightly different interval, spreading
	// reconnect attempts over time. This is instance-level jitter (not
	// per-attempt exponential backoff); paho's reconnect uses a fixed
	// interval, so we randomize it once at Init time.
	if t.connectRetryInterval > 0 {
		jitter := 0.8 + rand.Float64()*0.4 // 0.8–1.2 → ±20%
		t.connectRetryInterval = time.Duration(float64(t.connectRetryInterval) * jitter)
	}

	// Operation timeouts. These are per-operation MQTT timeouts (waiting
	// for a Subscribe/Publish token to complete), not the generic transport
	// I/O timeout, so they keep their own literals rather than reusing
	// core.DefaultTransportTimeout.
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

	// Parse command and forward settings
	t.parseCommandSettings(settings)

	// Initialize the replay-detection cache. When a command secret is
	// configured, each authenticated command's hash is recorded so that
	// the same message cannot be accepted twice within the skew window.
	// The cache is bounded to 10,000 entries and entries expire after
	// 2× the skew window (to cover clock drift on both sides).
	if t.commandSecret != "" && t.commandMaxSkew > 0 {
		t.replayCache = newReplayWindow(2*t.commandMaxSkew, 10000)
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

	// Parse TLS settings (optional).  When the broker URL uses a TLS scheme
	// (mqtts://, tls://, ssl://, ...) or any TLS file is set, a *tls.Config
	// is built and applied to the MQTT client at Start time.  This enables
	// encrypted connections and optional mutual TLS (mTLS) using a client
	// certificate/key pair, with server verification against a custom CA.
	// When none of those conditions hold, no TLS config is built and the
	// connection stays plaintext (backward compatible).
	t.tlsCertFile = util.GetStringSetting(settings, "tls-cert-file", "")
	t.tlsKeyFile = util.GetStringSetting(settings, "tls-key-file", "")
	t.tlsCAFile = util.GetStringSetting(settings, "tls-ca-file", "")
	tlsCfg, err := t.buildTLSConfig()
	if err != nil {
		return err
	}
	t.tlsConfig = tlsCfg

	slog.Info("mqtt transport initialized",
		"name", t.name,
		"broker", t.broker,
		"client-id", t.clientID,
		"tls", t.tlsConfig != nil,
	)

	return nil
}

// parseCommandSettings extracts command-related settings from the MQTT
// transport's settings map. This is split out from Init to keep that
// function's cyclomatic complexity manageable.
func (t *MQTTTransport) parseCommandSettings(settings map[string]any) {
	t.commandTopic = util.GetStringSetting(settings, "command-topic", "")
	t.commandSecret = util.GetStringSetting(settings, "command-secret", "")
	t.commandForwardTopic = util.GetStringSetting(settings, "command-forward-topic", "")
	t.commandForwardSecret = util.GetStringSetting(settings, "command-forward-secret", "")

	// Replay-protection settings for authenticated command messages.
	//   command-max-skew: how far a command's "timestamp" field (Unix
	//     milliseconds) may drift from the current time before the
	//     command is rejected as a replay/stale message (default 5m).
	//   command-strict-replay: when true, authenticated commands that do
	//     not include a timestamp are rejected outright; when false
	//     (default), they are accepted with a warning for backward
	//     compatibility with senders that predate replay protection.
	t.commandMaxSkew = util.GetDurationSetting(settings, "command-max-skew", 5*time.Minute)
	t.commandStrictReplay = util.GetBoolSetting(settings, "command-strict-replay", false)
}

func (t *MQTTTransport) Start(ctx context.Context) (err error) {
	t.ctx, t.cancel = context.WithCancel(ctx)

	// If Start returns an error, cancel the derived context to prevent
	// resource leaks. When the transport is not registered to the engine
	// (e.g. connection failed with autoReconnect=false), the caller may
	// never call Stop(), so the derived context and its associated
	// resources would leak. On success, the context stays active for the
	// transport's lifetime and is cancelled by Stop().
	defer func() {
		if err != nil && t.cancel != nil {
			t.cancel()
		}
	}()

	// Warn if a command topic is configured without a command secret: in
	// that mode command messages are accepted unauthenticated, which is
	// Fail-closed: if a command topic is configured but no command-secret is
	// set, reject startup. Accepting unauthenticated command messages would
	// allow any MQTT client to issue PLC write commands.
	if t.commandTopic != "" && t.commandSecret == "" {
		return fmt.Errorf("mqtt transport %s: command-topic %q is configured but command-secret is empty; refusing to start without command authentication (set command-secret or remove command-topic)",
			t.name, t.commandTopic)
	}
	// When command authentication is enabled, surface the replay-protection
	// posture so operators can confirm whether timestamp enforcement and
	// strict mode are active.
	if t.commandTopic != "" && t.commandSecret != "" {
		slog.Info("mqtt command authentication enabled",
			"name", t.name, "topic", t.commandTopic,
			"max-skew", t.commandMaxSkew, "strict-replay", t.commandStrictReplay,
			"replay-cache", t.replayCache != nil)
	}

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

	// Apply TLS configuration when present.  paho only uses this config for
	// TLS-scheme brokers (mqtts://, tls://, ssl://, ...); for plaintext
	// brokers it is ignored.  buildTLSConfig guarantees a non-nil config
	// only when TLS is actually required.
	if t.tlsConfig != nil {
		opts.SetTLSConfig(t.tlsConfig)
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
		// If neither auto-reconnect nor connect-retry is enabled, there is
		// no background mechanism that will ever establish the connection,
		// so reporting success would leave the engine believing the
		// transport is healthy while it is permanently stuck. Surface the
		// failure instead. When a retry mechanism is enabled, keep the
		// historical behaviour and let the background retry recover.
		if !t.autoReconnect && !t.connectRetry {
			t.mu.Lock()
			t.state = core.StateError
			t.mu.Unlock()
			slog.Error("mqtt connect timed out and auto-reconnect is disabled",
				"name", t.name, "broker", t.broker)
			return fmt.Errorf("mqtt: connect to %s timed out after %s", t.broker, t.connectTimeout)
		}
		slog.Warn("mqtt connect timed out, will retry in background",
			"name", t.name, "broker", t.broker)
		// Don't fail — ConnectRetry will handle it
		return nil
	}

	if err := token.Error(); err != nil {
		if !t.autoReconnect && !t.connectRetry {
			t.mu.Lock()
			t.state = core.StateError
			t.mu.Unlock()
			slog.Error("mqtt connect failed and auto-reconnect is disabled",
				"name", t.name, "error", err)
			return fmt.Errorf("mqtt: connect to %s failed: %w", t.broker, err)
		}
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
		t.handleCommandMessage(msg.Topic(), msg.Payload())
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

	// Stop the replay-cache eviction goroutine to prevent a goroutine
	// leak when the transport is shut down.
	if t.replayCache != nil {
		t.replayCache.close()
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

// ForwardCommand publishes a write command to the command-forward-topic,
// enabling chained-core command passthrough. Relay nodes that have no
// local driver for the command target use this to forward commands to
// downstream nodes.
//
// When command-forward-secret is configured, the forwarded command is
// signed with HMAC-SHA256 and includes a fresh timestamp so the
// downstream node can authenticate it and enforce replay protection.
// When no secret is configured, the command is forwarded unsigned
// (backward compatible with unauthenticated command setups).
func (t *MQTTTransport) ForwardCommand(ctx context.Context, cmd core.WriteCommand) error {
	if t.commandForwardTopic == "" {
		return fmt.Errorf("no command-forward-topic configured on transport %s", t.name)
	}
	if t.client == nil || !t.client.IsConnected() {
		return fmt.Errorf("mqtt transport %s is not connected", t.name)
	}

	// Marshal the command to JSON
	payload, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal forwarded command: %w", err)
	}

	// When a forward secret is configured, sign the command with
	// HMAC-SHA256 and add a timestamp for downstream replay protection.
	if t.commandForwardSecret != "" {
		// Parse into a map to add timestamp and signature fields
		var m map[string]json.RawMessage
		if err := json.Unmarshal(payload, &m); err != nil {
			return fmt.Errorf("failed to enrich forwarded command: %w", err)
		}
		// Add fresh timestamp (Unix milliseconds)
		tsBytes, _ := json.Marshal(time.Now().UnixMilli())
		m["timestamp"] = tsBytes
		// Re-marshal to get the canonical bytes (without signature)
		signedPayload, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("failed to re-marshal signed forwarded command: %w", err)
		}
		// Compute signature over canonical signing bytes
		sig := computeHMACSignature(t.commandForwardSecret, commandSigningBytes(signedPayload))
		// Add signature to the payload
		var finalM map[string]json.RawMessage
		if err := json.Unmarshal(signedPayload, &finalM); err != nil {
			return fmt.Errorf("failed to add signature to forwarded command: %w", err)
		}
		sigBytes, _ := json.Marshal(sig)
		finalM["X-Signature"] = sigBytes
		payload, err = json.Marshal(finalM)
		if err != nil {
			return fmt.Errorf("failed to finalize signed forwarded command: %w", err)
		}
	}

	// Publish with timeout
	token := t.client.Publish(t.commandForwardTopic, t.qos, false, payload)
	if ok := token.WaitTimeout(t.publishTimeout); !ok {
		return fmt.Errorf("mqtt forward publish timed out for topic %s", t.commandForwardTopic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt forward publish failed: %w", err)
	}

	t.published.Add(1)
	t.mu.Lock()
	t.lastPublish = time.Now()
	t.mu.Unlock()

	slog.Info("mqtt command forwarded",
		"topic", t.commandForwardTopic,
		"driver", cmd.Driver,
		"tag", cmd.Tag,
		"signed", t.commandForwardSecret != "",
	)

	return nil
}

func (t *MQTTTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	if len(points) == 0 {
		return nil
	}

	if t.client == nil || !t.client.IsConnected() {
		t.failed.Add(uint64(len(points)))
		return fmt.Errorf("mqtt transport %s is not connected", t.name)
	}

	// Concurrent publish: fire all tokens, then wait for all to complete.
	// This reduces batch latency from N×RTT to ~RTT (D8).
	type publishResult struct {
		index int
		err   error
	}
	results := make([]publishResult, len(points))
	var wg sync.WaitGroup
	wg.Add(len(points))

	for i := range points {
		go func(idx int) {
			defer wg.Done()
			results[idx] = publishResult{index: idx, err: t.Publish(ctx, points[idx])}
		}(i)
	}
	wg.Wait()

	var firstErr error
	for _, r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("publishBatch: point %d/%d: %w", r.index+1, len(points), r.err)
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
func (t *MQTTTransport) Type() string { return TypeName }

func (t *MQTTTransport) Status() core.TransportStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	queueSize := 0
	if t.client != nil {
		// No direct queue size API, but we track pending commands
		queueSize = len(t.commandCh)
	}

	return core.TransportStatus{
		Name:            t.name,
		Type:            TypeName,
		State:           t.state,
		Published:       t.published.Load(),
		Failed:          t.failed.Load(),
		Received:        t.received.Load(),
		LastPublish:     t.lastPublish,
		QueueSize:       queueSize,
		DroppedCommands: t.droppedCommands.Load(),
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
