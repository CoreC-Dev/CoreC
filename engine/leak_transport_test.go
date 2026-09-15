package engine

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// mqttLikeTransport mimics the MQTT transport's behavior of NOT closing
// its command/data channels on Stop — the exact pattern that caused the
// goroutine leak before the per-transport context fix.
type mqttLikeTransport struct {
	name     string
	cmdCh    chan core.WriteCommand
	dataCh   chan core.DataPoint
	stopOnce sync.Once
}

func newMQTTLikeTransport(name string) *mqttLikeTransport {
	return &mqttLikeTransport{
		name:   name,
		cmdCh:  make(chan core.WriteCommand, 16),
		dataCh: make(chan core.DataPoint, 16),
	}
}

func (t *mqttLikeTransport) Init(_ context.Context, _ core.TransportConfig) error { return nil }
func (t *mqttLikeTransport) Start(_ context.Context) error                        { return nil }
func (t *mqttLikeTransport) Stop() error {
	t.stopOnce.Do(func() {
		// Deliberately do NOT close cmdCh or dataCh — this is what
		// MQTTTransport.Stop does (see publisher.go comment).
	})
	return nil
}
func (t *mqttLikeTransport) Publish(_ context.Context, _ core.DataPoint) error        { return nil }
func (t *mqttLikeTransport) PublishBatch(_ context.Context, _ []core.DataPoint) error { return nil }
func (t *mqttLikeTransport) OnCommand() <-chan core.WriteCommand                      { return t.cmdCh }
func (t *mqttLikeTransport) OnData() <-chan core.DataPoint                            { return t.dataCh }
func (t *mqttLikeTransport) Name() string                                             { return t.name }
func (t *mqttLikeTransport) Type() string                                             { return "mqtt-leak-test" }
func (t *mqttLikeTransport) Status() core.TransportStatus                             { return core.TransportStatus{} }

// registerOnce ensures we only register the mock transport factory once.
var registerOnce sync.Once

func registerLeakTestTransport() {
	registerOnce.Do(func() {
		core.RegisterTransport("mqtt-leak-test", func(cfg core.TransportConfig) (core.Transport, error) {
			return newMQTTLikeTransport(cfg.Name), nil
		})
	})
}

// TestRemoveTransportNoGoroutineLeak verifies that removing a transport
// whose Stop() does NOT close its channels (like MQTT) does not leak
// the command/data listener goroutines. This is a regression test for
// the per-transport context cancellation fix.
func TestRemoveTransportNoGoroutineLeak(t *testing.T) {
	registerLeakTestTransport()

	e := New()
	cfg := &core.Config{}
	if err := e.Start(context.Background(), cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer e.Stop()

	runtime.GC()
	before := runtime.NumGoroutine()

	// Add and remove a mqtt-like transport 10 times.
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("leak-test-%d", i)
		if err := e.AddTransport(core.TransportConfig{
			Name:     name,
			Type:     "mqtt-leak-test",
			Settings: map[string]any{},
		}); err != nil {
			t.Fatalf("AddTransport %d failed: %v", i, err)
		}

		// Give the listener goroutines time to start.
		time.Sleep(10 * time.Millisecond)

		if err := e.RemoveTransport(name); err != nil {
			t.Fatalf("RemoveTransport %d failed: %v", i, err)
		}
	}

	// Wait for goroutines to settle.
	time.Sleep(200 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	delta := after - before
	if delta > 2 {
		t.Errorf("goroutine leak: before=%d after=%d delta=%d (expected <=2)", before, after, delta)
	}
}
