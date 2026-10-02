package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// ─── Mock paho types for discovery tests ───────────────────────────

type discMockToken struct {
	doneCh chan struct{}
	err    error
}

func newDiscMockToken(err error) *discMockToken {
	t := &discMockToken{doneCh: make(chan struct{}), err: err}
	close(t.doneCh)
	return t
}

func (t *discMockToken) Wait() bool                     { <-t.doneCh; return true }
func (t *discMockToken) WaitTimeout(time.Duration) bool { <-t.doneCh; return true }
func (t *discMockToken) Done() <-chan struct{}          { return t.doneCh }
func (t *discMockToken) Error() error                   { return t.err }

type discMockMessage struct {
	topic   string
	payload []byte
}

func (m *discMockMessage) Duplicate() bool   { return false }
func (m *discMockMessage) Qos() byte         { return 0 }
func (m *discMockMessage) Retained() bool    { return false }
func (m *discMockMessage) Topic() string     { return m.topic }
func (m *discMockMessage) MessageID() uint16 { return 0 }
func (m *discMockMessage) Payload() []byte   { return m.payload }
func (m *discMockMessage) Ack()              {}

// discMockClient records Publish/Subscribe/Connect/Disconnect calls.
type discMockClient struct {
	mu         sync.Mutex
	publishes  []discPublish
	subscribed []string
	connected  bool
}

type discPublish struct {
	topic   string
	payload []byte
}

func (c *discMockClient) IsConnected() bool      { c.mu.Lock(); defer c.mu.Unlock(); return c.connected }
func (c *discMockClient) IsConnectionOpen() bool { return c.IsConnected() }
func (c *discMockClient) Connect() pahomqtt.Token {
	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	return newDiscMockToken(nil)
}
func (c *discMockClient) Disconnect(quiesce uint) {
	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
}
func (c *discMockClient) Publish(topic string, qos byte, retained bool, payload interface{}) pahomqtt.Token {
	c.mu.Lock()
	c.publishes = append(c.publishes, discPublish{topic: topic, payload: payload.([]byte)})
	c.mu.Unlock()
	return newDiscMockToken(nil)
}
func (c *discMockClient) Subscribe(topic string, qos byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	c.mu.Lock()
	c.subscribed = append(c.subscribed, topic)
	c.mu.Unlock()
	return newDiscMockToken(nil)
}
func (c *discMockClient) SubscribeMultiple(filters map[string]byte, callback pahomqtt.MessageHandler) pahomqtt.Token {
	return newDiscMockToken(nil)
}
func (c *discMockClient) Unsubscribe(topics ...string) pahomqtt.Token             { return newDiscMockToken(nil) }
func (c *discMockClient) AddRoute(topic string, callback pahomqtt.MessageHandler) {}
func (c *discMockClient) OptionsReader() pahomqtt.ClientOptionsReader {
	return pahomqtt.ClientOptionsReader{}
}

// newTestDiscovery builds a Discovery with a mock client factory for testing.
func newTestDiscovery(nodeID, role string, subscribe []string, brokers []brokerEndpoint, addTransport func(core.TransportConfig) error) (*Discovery, *discMockClient) {
	mc := &discMockClient{}
	d := NewDiscovery(core.NodeConfig{ID: nodeID, Role: role, Subscribe: subscribe}, brokers, addTransport)
	d.clientFactory = func(opts *pahomqtt.ClientOptions) pahomqtt.Client { return mc }
	return d, mc
}

// ─── onDiscoveryMessage tests ──────────────────────────────────────

func TestOnDiscoveryMessageUpdatesRegistry(t *testing.T) {
	d, _ := newTestDiscovery("node-a", "edge", nil, nil, nil)

	payload, _ := json.Marshal(nodeInfo{
		ID:      "node-b",
		Role:    "cloud",
		Publish: &endpoint{Type: "mqtt", Topic: "data/b"},
	})
	msg := &discMockMessage{topic: "corec/_discovery/node-b", payload: payload}
	d.onDiscoveryMessage(nil, msg)

	nodes := d.GetDiscoveredNodes()
	if len(nodes) != 1 || nodes[0].ID != "node-b" {
		t.Fatalf("expected registry to contain node-b, got %v", nodes)
	}
	if nodes[0].Role != "cloud" {
		t.Errorf("expected role cloud, got %s", nodes[0].Role)
	}
}

func TestOnDiscoveryMessageIgnoresSelf(t *testing.T) {
	d, _ := newTestDiscovery("node-a", "edge", nil, nil, nil)

	payload, _ := json.Marshal(nodeInfo{ID: "node-a", Role: "edge"})
	msg := &discMockMessage{topic: "corec/_discovery/node-a", payload: payload}
	d.onDiscoveryMessage(nil, msg)

	if len(d.GetDiscoveredNodes()) != 0 {
		t.Error("expected self-heartbeat to be ignored")
	}
}

func TestOnDiscoveryMessageBadJSON(t *testing.T) {
	d, _ := newTestDiscovery("node-a", "edge", nil, nil, nil)

	msg := &discMockMessage{topic: "corec/_discovery/x", payload: []byte("not-json")}
	d.onDiscoveryMessage(nil, msg)

	if len(d.GetDiscoveredNodes()) != 0 {
		t.Error("expected no registry entry for bad JSON")
	}
}

// ─── reconcile tests ───────────────────────────────────────────────

func TestReconcileAutoAddsTransport(t *testing.T) {
	var added []core.TransportConfig
	addTransport := func(tc core.TransportConfig) error {
		added = append(added, tc)
		return nil
	}

	brokers := []brokerEndpoint{
		{Broker: "tcp://127.0.0.1:1883", Publish: &endpoint{Type: "mqtt", Topic: "data/a"}},
	}
	d, _ := newTestDiscovery("node-a", "edge", []string{"node-b"}, brokers, addTransport)

	// Simulate receiving a heartbeat from node-b.
	d.regMu.Lock()
	d.registry["node-b"] = &nodeInfo{
		ID:       "node-b",
		Role:     "cloud",
		Publish:  &endpoint{Type: "mqtt", Topic: "data/b"},
		lastSeen: time.Now(),
	}
	d.regMu.Unlock()

	d.reconcile()

	if len(added) != 1 {
		t.Fatalf("expected 1 transport added, got %d", len(added))
	}
	if added[0].Name != "auto-node-b" {
		t.Errorf("expected transport name auto-node-b, got %s", added[0].Name)
	}
	if added[0].Settings["data-topic"] != "data/b" {
		t.Errorf("expected data-topic data/b, got %v", added[0].Settings["data-topic"])
	}
}

func TestReconcileIdempotent(t *testing.T) {
	callCount := 0
	addTransport := func(tc core.TransportConfig) error {
		callCount++
		return nil
	}

	brokers := []brokerEndpoint{
		{Broker: "tcp://127.0.0.1:1883", Publish: &endpoint{Type: "mqtt", Topic: "data/a"}},
	}
	d, _ := newTestDiscovery("node-a", "edge", []string{"node-b"}, brokers, addTransport)

	d.regMu.Lock()
	d.registry["node-b"] = &nodeInfo{
		ID:       "node-b",
		Publish:  &endpoint{Type: "mqtt", Topic: "data/b"},
		lastSeen: time.Now(),
	}
	d.regMu.Unlock()

	d.reconcile()
	d.reconcile() // second call should not re-add

	if callCount != 1 {
		t.Errorf("expected addTransport called once (idempotent), got %d", callCount)
	}
}

func TestReconcileEvictsStaleNodes(t *testing.T) {
	addTransport := func(tc core.TransportConfig) error { return nil }
	d, _ := newTestDiscovery("node-a", "edge", []string{"node-b"}, nil, addTransport)

	// Insert a stale node (last seen well beyond 3 heartbeat intervals).
	d.regMu.Lock()
	d.registry["node-b"] = &nodeInfo{
		ID:       "node-b",
		lastSeen: time.Now().Add(-time.Duration(defaultHeartbeatSecs*4) * time.Second),
	}
	d.regMu.Unlock()

	d.reconcile()

	if len(d.GetDiscoveredNodes()) != 0 {
		t.Error("expected stale node to be evicted")
	}
}

func TestReconcileSkipsNonMQTTPublish(t *testing.T) {
	callCount := 0
	addTransport := func(tc core.TransportConfig) error {
		callCount++
		return nil
	}
	d, _ := newTestDiscovery("node-a", "edge", []string{"node-b"}, nil, addTransport)

	d.regMu.Lock()
	d.registry["node-b"] = &nodeInfo{
		ID:       "node-b",
		Publish:  &endpoint{Type: "http", URL: "http://x"},
		lastSeen: time.Now(),
	}
	d.regMu.Unlock()

	d.reconcile()

	if callCount != 0 {
		t.Error("expected no transport for non-mqtt publish endpoint")
	}
}

// ─── GetDiscoveredNodes tests ──────────────────────────────────────

func TestGetDiscoveredNodesSnapshot(t *testing.T) {
	d, _ := newTestDiscovery("node-a", "edge", nil, nil, nil)

	d.regMu.Lock()
	d.registry["b"] = &nodeInfo{ID: "b", lastSeen: time.Now()}
	d.registry["c"] = &nodeInfo{ID: "c", lastSeen: time.Now()}
	d.regMu.Unlock()

	nodes := d.GetDiscoveredNodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	// Mutating the returned slice must not affect the registry.
	nodes[0].ID = "mutated"
	fresh := d.GetDiscoveredNodes()
	for _, n := range fresh {
		if n.ID == "mutated" {
			t.Error("GetDiscoveredNodes did not return a defensive copy")
		}
	}
}

// ─── sendHeartbeats tests ──────────────────────────────────────────

func TestSendHeartbeatsPublishes(t *testing.T) {
	brokers := []brokerEndpoint{
		{Broker: "tcp://127.0.0.1:1883", Publish: &endpoint{Type: "mqtt", Topic: "data/a"}},
	}
	d, mc := newTestDiscovery("node-a", "edge", nil, brokers, nil)
	// sendHeartbeats looks up clients by broker URL; inject the mock.
	d.clients["tcp://127.0.0.1:1883"] = mc

	d.sendHeartbeats()

	mc.mu.Lock()
	defer mc.mu.Unlock()
	if len(mc.publishes) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(mc.publishes))
	}
	expectedTopic := "corec/_discovery/node-a"
	if mc.publishes[0].topic != expectedTopic {
		t.Errorf("expected topic %s, got %s", expectedTopic, mc.publishes[0].topic)
	}
	var info nodeInfo
	if err := json.Unmarshal(mc.publishes[0].payload, &info); err != nil {
		t.Fatalf("failed to unmarshal heartbeat: %v", err)
	}
	if info.ID != "node-a" || info.Role != "edge" {
		t.Errorf("unexpected heartbeat content: %+v", info)
	}
}

// ─── Start/Stop lifecycle tests ────────────────────────────────────

func TestDiscoveryStartStop(t *testing.T) {
	brokers := []brokerEndpoint{
		{Broker: "tcp://127.0.0.1:1883", Publish: &endpoint{Type: "mqtt", Topic: "data/a"}},
	}
	d, mc := newTestDiscovery("node-a", "edge", nil, brokers, nil)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// The heartbeat loop sends immediately on start; wait briefly for it.
	time.Sleep(200 * time.Millisecond)

	d.Stop()

	mc.mu.Lock()
	defer mc.mu.Unlock()
	if len(mc.publishes) == 0 {
		t.Error("expected at least one heartbeat publish after Start")
	}
	if mc.connected {
		t.Error("expected client to be disconnected after Stop")
	}
}

func TestDiscoveryStopWithoutStart(t *testing.T) {
	// Stop must be safe to call even if Start was never called (cancel is nil).
	d, _ := newTestDiscovery("node-a", "edge", nil, nil, nil)
	d.Stop() // must not panic
}
