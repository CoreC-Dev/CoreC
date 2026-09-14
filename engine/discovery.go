package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/CoreC-Dev/CoreC/core"
)

const (
	discoveryTopicPrefix = "corec/_discovery"
	defaultHeartbeatSecs = 5
)

// nodeInfo is the heartbeat payload broadcast by each node on each broker.
type nodeInfo struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Subscribe []string  `json:"subscribe,omitempty"`
	Publish   *endpoint `json:"publish,omitempty"` // how others receive data FROM this node
	Receive   *endpoint `json:"receive,omitempty"` // how others send data TO this node
	Timestamp int64     `json:"ts"`
}

// endpoint describes a data channel.
type endpoint struct {
	Type  string `json:"type"`            // "mqtt" or "http"
	Topic string `json:"topic,omitempty"` // for mqtt: subscription pattern
	URL   string `json:"url,omitempty"`   // for http: full URL
}

// brokerEndpoint pairs a broker URL with the publish endpoint active on it.
// A node may publish on some brokers and not others.
type brokerEndpoint struct {
	Broker  string
	Publish *endpoint // nil if this node doesn't publish on this broker
	Receive *endpoint // nil if this node doesn't receive on this broker
}

// Discovery manages topology auto-discovery via MQTT heartbeats.
// Each broker gets its own lightweight MQTT client that publishes the
// node's identity and subscribes to other nodes' heartbeats.
type Discovery struct {
	nodeID    string
	role      string
	subscribe []string

	// Per-broker clients and the endpoints to advertise on each.
	brokers []brokerEndpoint
	clients map[string]pahomqtt.Client // broker URL → client

	// Registry of discovered nodes: nodeID → info.
	registry map[string]*nodeInfo
	regMu    sync.RWMutex

	// addTransport is the engine callback to dynamically add a transport.
	addTransport func(core.TransportConfig) error

	// autoAdded tracks "broker|upstreamID" keys we've already handled.
	autoAdded map[string]bool
	addedMu   sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewDiscovery creates a discovery module. brokers lists each broker this
// node connects to and what endpoints it advertises on each.
func NewDiscovery(node core.NodeConfig, brokers []brokerEndpoint, addTransport func(core.TransportConfig) error) *Discovery {
	return &Discovery{
		nodeID:       node.ID,
		role:         node.Role,
		subscribe:    node.Subscribe,
		brokers:      brokers,
		clients:      make(map[string]pahomqtt.Client),
		registry:     make(map[string]*nodeInfo),
		addTransport: addTransport,
		autoAdded:    make(map[string]bool),
	}
}

// Start connects to each broker, subscribes to the discovery topic, and
// begins broadcasting heartbeats.
func (d *Discovery) Start(ctx context.Context) error {
	d.ctx, d.cancel = context.WithCancel(ctx)

	for _, be := range d.brokers {
		clientID := fmt.Sprintf("corec-disc-%s-%s", d.nodeID, sanitizeBroker(be.Broker))
		opts := pahomqtt.NewClientOptions()
		opts.AddBroker(be.Broker)
		opts.SetClientID(clientID)
		opts.SetAutoReconnect(true)
		opts.SetCleanSession(true)
		opts.SetConnectRetry(true)
		opts.SetConnectRetryInterval(5 * time.Second)
		opts.SetOrderMatters(false)

		broker := be.Broker
		opts.SetOnConnectHandler(func(c pahomqtt.Client) {
			slog.Info("discovery connected", "broker", broker, "node", d.nodeID)
			// Subscribe to all node heartbeats on this broker.
			topic := discoveryTopicPrefix + "/+"
			token := c.Subscribe(topic, 0, d.onDiscoveryMessage)
			token.WaitTimeout(5 * time.Second)
		})

		client := pahomqtt.NewClient(opts)
		token := client.Connect()
		if !token.WaitTimeout(10 * time.Second) {
			slog.Warn("discovery connect timed out, will retry",
				"broker", be.Broker, "node", d.nodeID)
		}
		d.clients[be.Broker] = client
	}

	// Start heartbeat and reconciliation loops.
	d.wg.Add(2)
	go d.heartbeatLoop()
	go d.reconcileLoop()

	slog.Info("discovery started",
		"node", d.nodeID,
		"role", d.role,
		"brokers", len(d.brokers),
		"subscribe", d.subscribe)
	return nil
}

// Stop disconnects all broker clients and waits for goroutines to exit.
func (d *Discovery) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()

	for broker, client := range d.clients {
		client.Disconnect(500) // 500ms quiesce
		slog.Debug("discovery disconnected", "broker", broker)
	}
	slog.Info("discovery stopped", "node", d.nodeID)
}

// heartbeatLoop publishes this node's info to each broker periodically.
func (d *Discovery) heartbeatLoop() {
	defer d.wg.Done()
	ticker := time.NewTicker(time.Duration(defaultHeartbeatSecs) * time.Second)
	defer ticker.Stop()

	d.sendHeartbeats() // send immediately on start
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.sendHeartbeats()
		}
	}
}

// sendHeartbeats publishes the node info to each broker with the
// appropriate endpoint for that broker.
func (d *Discovery) sendHeartbeats() {
	for _, be := range d.brokers {
		client, ok := d.clients[be.Broker]
		if !ok {
			continue
		}

		info := nodeInfo{
			ID:        d.nodeID,
			Role:      d.role,
			Subscribe: d.subscribe,
			Publish:   be.Publish,
			Receive:   be.Receive,
			Timestamp: time.Now().Unix(),
		}

		payload, err := json.Marshal(info)
		if err != nil {
			slog.Error("discovery: failed to marshal heartbeat", "error", err)
			continue
		}

		topic := fmt.Sprintf("%s/%s", discoveryTopicPrefix, d.nodeID)
		token := client.Publish(topic, 0, true, payload) // retained so late joiners see it
		token.WaitTimeout(3 * time.Second)
	}
}

// onDiscoveryMessage handles incoming heartbeats from other nodes.
func (d *Discovery) onDiscoveryMessage(client pahomqtt.Client, msg pahomqtt.Message) {
	var info nodeInfo
	if err := json.Unmarshal(msg.Payload(), &info); err != nil {
		slog.Debug("discovery: failed to unmarshal heartbeat", "error", err)
		return
	}

	// Ignore our own heartbeats.
	if info.ID == d.nodeID {
		return
	}

	d.regMu.Lock()
	d.registry[info.ID] = &info
	d.regMu.Unlock()

	slog.Debug("discovery: received heartbeat",
		"from", info.ID,
		"role", info.Role,
		"has-publish", info.Publish != nil)
}

// reconcileLoop periodically checks the registry against the subscribe
// list and auto-adds transports for newly discovered upstreams.
func (d *Discovery) reconcileLoop() {
	defer d.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.reconcile()
		}
	}
}

// reconcile checks each subscribed upstream and auto-adds a transport
// if the upstream is discovered on a broker we're connected to.
func (d *Discovery) reconcile() {
	d.regMu.RLock()
	// Snapshot registry to avoid holding the lock while adding transports.
	snapshot := make(map[string]*nodeInfo, len(d.registry))
	for k, v := range d.registry {
		snapshot[k] = v
	}
	d.regMu.RUnlock()

	for _, upstreamID := range d.subscribe {
		info, found := snapshot[upstreamID]
		if !found {
			continue
		}
		if info.Publish == nil || info.Publish.Type != "mqtt" {
			continue
		}

		// Find which broker we discovered this upstream on.
		// We try all our brokers and add a transport on the first one
		// where we haven't already added one for this upstream.
		for _, be := range d.brokers {
			key := be.Broker + "|" + upstreamID

			d.addedMu.Lock()
			if d.autoAdded[key] {
				d.addedMu.Unlock()
				continue
			}
			d.autoAdded[key] = true
			d.addedMu.Unlock()

			// Create an inbound transport to receive from this upstream.
			tc := core.TransportConfig{
				Name: fmt.Sprintf("auto-%s", upstreamID),
				Type: "mqtt",
				Settings: map[string]any{
					"broker":     be.Broker,
					"data-topic": info.Publish.Topic,
					"parser":     map[string]any{"type": "default"},
				},
			}

			if err := d.addTransport(tc); err != nil {
				slog.Error("discovery: failed to auto-add transport",
					"upstream", upstreamID,
					"broker", be.Broker,
					"error", err)
				// Allow retry on next reconcile.
				d.addedMu.Lock()
				delete(d.autoAdded, key)
				d.addedMu.Unlock()
				continue
			}

			slog.Info("discovery: auto-subscribed to upstream",
				"upstream", upstreamID,
				"broker", be.Broker,
				"topic", info.Publish.Topic)
			break // successfully added on this broker, don't try others
		}
	}
}

// GetDiscoveredNodes returns a snapshot of all known nodes.
func (d *Discovery) GetDiscoveredNodes() []nodeInfo {
	d.regMu.RLock()
	defer d.regMu.RUnlock()
	result := make([]nodeInfo, 0, len(d.registry))
	for _, info := range d.registry {
		result = append(result, *info)
	}
	return result
}

// sanitizeBroker turns a broker URL into a safe component for client IDs.
func sanitizeBroker(broker string) string {
	r := strings.NewReplacer(":", "_", "/", "_", ".", "-")
	return r.Replace(broker)
}

// topicTemplateToSubscriptionPattern converts a topic-template like
// "topo/edge-A/data/{{.Driver}}/{{.Tag}}" into a subscription wildcard
// "topo/edge-A/data/#".
func topicTemplateToSubscriptionPattern(topicTemplate string) string {
	idx := strings.Index(topicTemplate, "{{")
	if idx == -1 {
		// No template variables — the whole thing is a static topic.
		return strings.TrimSuffix(topicTemplate, "/") + "/#"
	}
	prefix := topicTemplate[:idx]
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return "#"
	}
	return prefix + "/#"
}
