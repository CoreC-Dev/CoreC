// Package mqtt implements the MQTT transport for CoreC.
package mqtt

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math/rand/v2"
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

// TypeName identifies this transport's protocol, returned by Type() and
// embedded in Status().
const TypeName = "mqtt"

// MQTTTransport implements core.Transport for MQTT protocol.
type MQTTTransport struct {
	mu sync.RWMutex

	name   string
	config core.TransportConfig

	client pahomqtt.Client

	broker   string
	clientID string
	username string
	password string
	qos      byte
	retained bool

	// Last Will and Testament: when configured,
	// the broker publishes the will message on this topic if the client
	// disconnects ungracefully (crash, network cut, power loss) — the
	// standard edge-gateway liveness signal. Empty will-topic disables
	// it.
	willTopic            string
	willPayload          []byte
	willQoS              byte
	willRetain           bool
	topicTemplate        *template.Template
	commandTopic         string        // MQTT topic to subscribe for write commands
	commandSecret        string        // HMAC-SHA256 secret for authenticating command messages; empty = not configured, Start refuses to start when command-topic is set and this is empty
	commandForwardTopic  string        // MQTT topic to publish forwarded commands (chained-core passthrough)
	commandForwardSecret string        // HMAC-SHA256 secret for signing forwarded commands; empty = unsigned
	commandMaxSkew       time.Duration // max allowed timestamp drift for replay protection; default 5m; explicit 0s disables the replay cache and freshness check (not recommended)
	commandStrictReplay  bool          // reject commands without a timestamp when true
	dataTopic            string        // topic to subscribe for incoming data (chained-core inbound)
	dataParser           parser.Parser

	// TLS settings (mqtts://, tls://, ssl://, ...).  Empty file paths with
	// a plaintext broker = no TLS.  Built into
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

	state core.ConnState

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

	// replayCache is a bounded deduplication cache of seen command message
	// hashes; TTL and capacity are set in Init.
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

	if broker, ok := settings["broker"].(string); ok {
		t.broker = broker
	} else {
		return fmt.Errorf("mqtt: broker is required")
	}

	if clientID, ok := settings["client-id"].(string); ok {
		t.clientID = clientID
	} else {
		t.clientID = fmt.Sprintf("%s-%s-%d", projectName, config.Name, time.Now().UnixMilli())
	}

	t.username = util.GetStringSetting(settings, "username", "")
	t.password = util.GetStringSetting(settings, "password", "")

	t.qos = byte(util.GetIntSetting(settings, "qos", 1))

	if retained, ok := settings["retained"].(bool); ok {
		t.retained = retained
	}

	t.keepAlive = util.GetDurationSetting(settings, "keep-alive", core.DefaultKeepAlive)
	t.connectTimeout = util.GetDurationSetting(settings, "connect-timeout", 10*time.Second)
	t.autoReconnect = util.GetBoolSetting(settings, "auto-reconnect", true)
	t.cleanSession = util.GetBoolSetting(settings, "clean-session", true)
	t.connectRetry = util.GetBoolSetting(settings, "connect-retry", true)
	t.connectRetryInterval = util.GetDurationSetting(settings, "connect-retry-interval", 5*time.Second)

	// A one-time ±20% jitter is applied to connect-retry-interval at Init;
	// paho reconnects at a fixed interval.
	if t.connectRetryInterval > 0 {
		jitter := 0.8 + rand.Float64()*0.4 // 0.8–1.2 → ±20%
		t.connectRetryInterval = time.Duration(float64(t.connectRetryInterval) * jitter)
	}

	// Per-operation MQTT timeouts: waiting for a Subscribe/Publish token
	// to complete.
	t.subscribeTimeout = util.GetDurationSetting(settings, "subscribe-timeout", 5*time.Second)
	t.publishTimeout = util.GetDurationSetting(settings, "publish-timeout", 5*time.Second)
	t.disconnectQuiesce = util.GetDurationSetting(settings, "disconnect-quiesce", time.Second)

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

	t.parseCommandSettings(settings)

	// Last Will and Testament: will-topic +
	// will-payload (both required to arm), will-qos (default 0),
	// will-retain (default true).
	t.willTopic = util.GetStringSetting(settings, "will-topic", "")
	if wp, ok := settings["will-payload"].(string); ok {
		t.willPayload = []byte(wp)
	}
	t.willQoS = byte(util.GetIntSetting(settings, "will-qos", 0))
	if util.GetBoolSetting(settings, "will-retain", true) {
		t.willRetain = true
	}

	// Replay-detection cache: when a command secret is configured and
	// command-max-skew > 0, each authenticated command's hash is recorded;
	// entries expire after 2× the skew window.
	if t.commandSecret != "" && t.commandMaxSkew > 0 {
		t.replayCache = newReplayWindow(2*t.commandMaxSkew, 10000)
	}

	if dataTopic, ok := settings["data-topic"].(string); ok && dataTopic != "" {
		t.dataTopic = dataTopic
		p, err := parser.New(settings)
		if err != nil {
			return fmt.Errorf("mqtt: invalid parser config: %w", err)
		}
		t.dataParser = p
	}

	// TLS settings (optional).  When the broker URL uses a TLS scheme
	// (mqtts://, tls://, ssl://, ...) or any TLS file is set, a *tls.Config
	// is built and applied to the MQTT client at Start time.  This enables
	// encrypted connections and optional mutual TLS (mTLS) using a client
	// certificate/key pair, with server verification against a custom CA.
	// When none of those conditions hold, no TLS config is built and the
	// connection stays plaintext.
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
// transport's settings map.
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
	//     (default), they are accepted with a warning.
	t.commandMaxSkew = util.GetDurationSetting(settings, "command-max-skew", 5*time.Minute)
	t.commandStrictReplay = util.GetBoolSetting(settings, "command-strict-replay", false)
}
