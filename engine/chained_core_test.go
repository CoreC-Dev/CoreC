package engine

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// chainedTransport is a mock transport that has a real OnData channel,
// simulating an MQTT or HTTP transport receiving data from an upstream
// CoreC instance.  It lets us test the full chained-core path:
// transport.OnData → engine.startDataListener → DataBus → processingLoop.
type chainedTransport struct {
	name    string
	status  core.TransportStatus
	dataCh  chan core.DataPoint
	cmdCh   chan core.WriteCommand
	publish chan core.DataPoint
}

func newChainedTransport(name string) *chainedTransport {
	return &chainedTransport{
		name:    name,
		dataCh:  make(chan core.DataPoint, 10),
		cmdCh:   make(chan core.WriteCommand, 10),
		publish: make(chan core.DataPoint, 10),
	}
}

func (t *chainedTransport) Init(ctx context.Context, config core.TransportConfig) error { return nil }
func (t *chainedTransport) Start(ctx context.Context) error                             { return nil }
func (t *chainedTransport) Stop() error                                                 { return nil }
func (t *chainedTransport) Publish(ctx context.Context, point core.DataPoint) error {
	select {
	case t.publish <- point:
	default:
	}
	return nil
}
func (t *chainedTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	for i := range points {
		_ = t.Publish(ctx, points[i])
	}
	return nil
}
func (t *chainedTransport) OnCommand() <-chan core.WriteCommand { return t.cmdCh }
func (t *chainedTransport) OnData() <-chan core.DataPoint       { return t.dataCh }
func (t *chainedTransport) Name() string                        { return t.name }
func (t *chainedTransport) Type() string                        { return "chained" }
func (t *chainedTransport) Status() core.TransportStatus        { return t.status }

// TestChainedCore_OnDataToDataBus verifies the core chained-core
// flow: a DataPoint pushed into a transport's OnData channel appears
// in the engine's DataBus and is published to downstream transports.
//
// Data enters via a "virtual" inbound (OnData) and flows through the
// same processing pipeline as driver-sourced data.
func TestChainedCore_OnDataToDataBus(t *testing.T) {
	// Register a chained transport that captures published points.
	transportType := "chained-test-ondata"
	var ct *chainedTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		ct = newChainedTransport(config.Name)
		return ct, nil
	})

	// Register a mock driver (required for engine Start).
	core.RegisterDriver("mock-driver-chained", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{
				Name:     "dummy",
				Type:     "mock-driver-chained",
				Settings: map[string]any{},
				Tags:     []core.TagConfig{},
			},
		},
		Transports: []core.TransportConfig{
			{
				Name:     "chained-1",
				Type:     transportType,
				Settings: map[string]any{},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "chained-1"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine := New()
	if err := engine.Start(ctx, cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	// Simulate an upstream CoreC instance sending a DataPoint via MQTT/HTTP.
	upstreamPoint := core.DataPoint{
		Driver:    "upstream-plc",
		Tag:       "temperature",
		Value:     42.5,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}

	// Push into the transport's OnData channel — this simulates the
	// MQTT subscribe callback or HTTP webhook handler.
	ct.dataCh <- upstreamPoint

	// The engine's startDataListener goroutine should pick this up,
	// push it into DataBus, and the processingLoop should publish it
	// to the transport.  Wait for the published point.
	select {
	case published := <-ct.publish:
		if published.Tag != "temperature" {
			t.Errorf("expected tag 'temperature', got %q", published.Tag)
		}
		if v, ok := published.Value.(float64); !ok || v != 42.5 {
			t.Errorf("expected value 42.5, got %v", published.Value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: DataPoint from OnData did not reach Publish")
	}
}

// TestChainedCore_MultipleTransports verifies that multiple transports
// with OnData channels all feed into the same DataBus.
func TestChainedCore_MultipleTransports(t *testing.T) {
	transportType := "chained-multi"
	var transports []*chainedTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		ct := newChainedTransport(config.Name)
		transports = append(transports, ct)
		return ct, nil
	})

	core.RegisterDriver("mock-driver-multi", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{
				Name:     "dummy",
				Type:     "mock-driver-multi",
				Settings: map[string]any{},
				Tags:     []core.TagConfig{},
			},
		},
		Transports: []core.TransportConfig{
			{Name: "chain-a", Type: transportType, Settings: map[string]any{}},
			{Name: "chain-b", Type: transportType, Settings: map[string]any{}},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "chain-a"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine := New()
	if err := engine.Start(ctx, cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	if len(transports) != 2 {
		t.Fatalf("expected 2 transports, got %d", len(transports))
	}

	// Push from both
	transports[0].dataCh <- core.DataPoint{Driver: "src-a", Tag: "tag-a", Value: 1.0, Type: core.TypeFloat64, Timestamp: time.Now()}
	transports[1].dataCh <- core.DataPoint{Driver: "src-b", Tag: "tag-b", Value: 2.0, Type: core.TypeFloat64, Timestamp: time.Now()}

	// Both should be published (to both transports, since there's no rule filtering)
	receivedTags := make(map[string]bool)
	timeout := time.After(2 * time.Second)
	for len(receivedTags) < 2 {
		select {
		case p := <-transports[0].publish:
			receivedTags[p.Tag] = true
		case p := <-transports[1].publish:
			receivedTags[p.Tag] = true
		case <-timeout:
			t.Fatalf("timeout: only received tags %v", receivedTags)
		}
	}

	if !receivedTags["tag-a"] {
		t.Error("tag-a was not received")
	}
	if !receivedTags["tag-b"] {
		t.Error("tag-b was not received")
	}
}
