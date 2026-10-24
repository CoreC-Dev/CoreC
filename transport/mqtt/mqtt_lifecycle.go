// Package mqtt: transport lifecycle — Start (broker connect + subscribe),
// client-option assembly, TLS wiring, and the command/data subscriptions.
package mqtt

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/core"
	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

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

	// Build MQTT client options and connect.
	t.client = pahomqtt.NewClient(t.buildMQTTOptions())

	t.mu.Lock()
	t.state = core.StateConnecting
	t.mu.Unlock()

	if cerr := t.connectMQTT(); cerr != nil {
		return cerr
	}

	slog.Info("mqtt transport started", "name", t.name, "broker", t.broker)
	return nil
}

// buildMQTTOptions assembles the paho client options (broker, credentials,
// TLS, and connection lifecycle handlers) from the transport's configuration.
func (t *MQTTTransport) buildMQTTOptions() *pahomqtt.ClientOptions {
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
	// Arm the Last Will when configured: the broker publishes
	// it if this CoreC instance disappears without a DISCONNECT —
	// downstream systems get a broker-side liveness signal instead of
	// waiting for scrape gaps.
	if t.willTopic != "" && len(t.willPayload) > 0 {
		opts.SetBinaryWill(t.willTopic, t.willPayload, t.willQoS, t.willRetain)
		slog.Info("mqtt last-will armed",
			"name", t.name, "topic", t.willTopic, "qos", t.willQoS, "retain", t.willRetain)
	}
	// Apply TLS configuration when present. paho only uses this config for
	// TLS-scheme brokers (mqtts://, tls://, ssl://, ...); for plaintext
	// brokers it is ignored. buildTLSConfig guarantees a non-nil config
	// only when TLS is actually required.
	if t.tlsConfig != nil {
		opts.SetTLSConfig(t.tlsConfig)
	}

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
	return opts
}

// connectMQTT drives the initial connection attempt. When neither
// auto-reconnect nor connect-retry is enabled, a timeout or error is surfaced
// as a hard failure (no background mechanism would ever recover). When a
// retry mechanism is enabled, the historical behaviour is preserved: the
// failure is logged and nil is returned so the background retry recovers.
func (t *MQTTTransport) connectMQTT() error {
	token := t.client.Connect()
	if ok := token.WaitTimeout(t.connectTimeout); !ok {
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
		return nil
	}
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
