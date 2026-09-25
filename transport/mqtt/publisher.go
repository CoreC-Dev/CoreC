// Package mqtt implements the MQTT transport for CoreC.
package mqtt

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
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

// replayWindow is a bounded, time-expiring set of seen message hashes.
// It prevents true replay attacks (same message accepted twice) within
// the timestamp skew window. Entries older than the TTL are evicted
// lazily on each check and periodically via a background goroutine.
type replayWindow struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	ttl     time.Duration
	maxSize int
}

func newReplayWindow(ttl time.Duration, maxSize int) *replayWindow {
	rw := &replayWindow{
		seen:    make(map[string]time.Time),
		ttl:     ttl,
		maxSize: maxSize,
	}
	if ttl > 0 {
		go rw.evictLoop()
	}
	return rw
}

func (rw *replayWindow) evictLoop() {
	ticker := time.NewTicker(rw.ttl)
	defer ticker.Stop()
	for range ticker.C {
		rw.evictExpired()
	}
}

func (rw *replayWindow) evictExpired() {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	now := time.Now()
	for k, t := range rw.seen {
		if now.Sub(t) > rw.ttl {
			delete(rw.seen, k)
		}
	}
}

// checkAndAdd returns true if the hash is new (not seen before), false if
// it is a replay (already seen within the TTL window). It atomically adds
// the hash to the set if it is new.
func (rw *replayWindow) checkAndAdd(hash string) bool {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	now := time.Now()
	if _, ok := rw.seen[hash]; ok {
		return false // replay
	}
	// Bound the cache size: if at capacity, evict oldest entries.
	if len(rw.seen) >= rw.maxSize {
		var oldestKey string
		var oldestTime time.Time
		for k, t := range rw.seen {
			if oldestKey == "" || t.Before(oldestTime) {
				oldestKey = k
				oldestTime = t
			}
		}
		delete(rw.seen, oldestKey)
	}
	rw.seen[hash] = now
	return true
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

// tlsSchemes are the broker URL schemes that the paho MQTT client routes
// over TLS (see paho netconn.go).  Using one of these schemes is what
// actually enables TLS on the wire; SetTLSConfig only supplies the
// configuration that the TLS dial then uses.
var tlsSchemes = map[string]bool{
	"ssl": true, "tls": true, "mqtts": true, "mqtt+ssl": true, "tcps": true, "wss": true,
}

// buildTLSConfig constructs the *tls.Config for the MQTT client when TLS is
// required.  TLS is required when the broker URL uses a TLS scheme or when
// any TLS file setting (CA / client cert / key) is provided.  Returns nil
// (no TLS) when neither condition holds, preserving backward compatibility
// with plaintext mqtt:// / tcp:// brokers.
//
//   - tls-ca-file:     loads a CA certificate pool for server verification.
//     When omitted, Go uses the system root certificates.
//   - tls-cert-file + tls-key-file: loads a client certificate/key pair for
//     mutual TLS (mTLS).  Both must be set together.
//   - ServerName is derived from the broker URL host so that certificate
//     hostname verification works correctly.
func (t *MQTTTransport) buildTLSConfig() (*tls.Config, error) {
	// Normalize the broker the same way paho's AddBroker does so that any
	// broker paho accepts (including schemeless "host:port" forms) is
	// accepted here too, and we derive the same scheme paho would use.
	broker := t.broker
	if broker != "" && broker[0] == ':' {
		broker = "127.0.0.1" + broker
	}
	if !strings.Contains(broker, "://") {
		broker = "tcp://" + broker
	}

	u, err := url.Parse(broker)
	schemeTLS := err == nil && tlsSchemes[strings.ToLower(u.Scheme)]
	anyFile := t.tlsCAFile != "" || t.tlsCertFile != "" || t.tlsKeyFile != ""

	// Plaintext (or unparseable plaintext) with no TLS files: build no TLS
	// config.  This preserves the historical behaviour where Init accepts
	// any broker string and connection failures surface only at
	// Start/Connect time, so existing non-TLS setups are unaffected.
	if !schemeTLS && !anyFile {
		return nil, nil //nolint:nilnil // a nil *tls.Config with no error intentionally signals "no TLS needed"
	}

	// TLS is required (TLS scheme or TLS files set).  A valid parsed URL is
	// needed to derive ServerName for certificate hostname verification.
	if err != nil {
		return nil, fmt.Errorf("mqtt: invalid broker URL %q: %w", t.broker, err)
	}

	cfg := &tls.Config{
		ServerName: u.Hostname(),
		MinVersion: tls.VersionTLS12,
	}

	// CA certificate pool for server verification.  When omitted, Go falls
	// back to the system root certificates.
	if t.tlsCAFile != "" {
		pem, err := os.ReadFile(t.tlsCAFile)
		if err != nil {
			return nil, fmt.Errorf("mqtt: failed to read CA file %s: %w", t.tlsCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("mqtt: failed to parse CA certificate(s) from %s", t.tlsCAFile)
		}
		cfg.RootCAs = pool
	}

	// Client certificate + key for mutual TLS.  Both must be provided
	// together; specifying only one is a configuration error.
	if t.tlsCertFile != "" || t.tlsKeyFile != "" {
		if t.tlsCertFile == "" || t.tlsKeyFile == "" {
			return nil, fmt.Errorf("mqtt: tls-cert-file and tls-key-file must both be set for mutual TLS")
		}
		cert, err := tls.LoadX509KeyPair(t.tlsCertFile, t.tlsKeyFile)
		if err != nil {
			return nil, fmt.Errorf("mqtt: failed to load client key pair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	// Fail if TLS files were provided but the broker scheme is plaintext:
	// paho only applies the TLS config to TLS-scheme brokers, so the config
	// would silently have no effect on the wire. Failing here prevents a
	// misconfiguration where the operator thinks TLS is enabled but
	// credentials/commands are actually sent in plaintext.
	if !schemeTLS && anyFile {
		return nil, fmt.Errorf("mqtt: TLS files configured but broker scheme %q is not TLS; use mqtts:// or ssl:// scheme to enable TLS", u.Scheme)
	}

	return cfg, nil
}

func (t *MQTTTransport) Start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(ctx)

	// Warn if a command topic is configured without a command secret: in
	// that mode command messages are accepted unauthenticated, which is
	// insecure in any deployment where untrusted clients can publish to
	// the command topic. This preserves backward compatibility while
	// making the risk visible at startup.
	if t.commandTopic != "" && t.commandSecret == "" {
		slog.Warn("mqtt command topic has no command-secret; accepting unauthenticated command messages",
			"name", t.name, "topic", t.commandTopic)
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
	b, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return b
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
	var firstErr error
	for i := range points {
		if err := t.Publish(ctx, points[i]); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("publishBatch: point %d/%d: %w", i+1, len(points), err)
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
