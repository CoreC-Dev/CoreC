package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// --- Enhanced mocks for lifecycle tests ---

// mockDriverWithState tracks start/stop/restart calls.
type mockDriverWithState struct {
	mu         sync.Mutex
	name       string
	status     core.DriverStatus
	startCount int
	stopCount  int
	readCount  int
	writeCount int
	readVal    any
}

func (m *mockDriverWithState) Init(ctx context.Context, config core.DriverConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.name = config.Name
	m.status = core.DriverStatus{Name: config.Name, Type: config.Type, State: core.StateDisconnected}
	return nil
}
func (m *mockDriverWithState) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startCount++
	m.status = core.DriverStatus{Name: m.name, Type: "mock-state", State: core.StateConnected}
	return nil
}
func (m *mockDriverWithState) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCount++
	m.status = core.DriverStatus{Name: m.name, Type: "mock-state", State: core.StateDisconnected}
	return nil
}
func (m *mockDriverWithState) Restart(ctx context.Context, config core.DriverConfig) error {
	return nil
}
func (m *mockDriverWithState) Read(ctx context.Context, tags []string) ([]core.TagValue, error) {
	m.mu.Lock()
	m.readCount++
	val := m.readVal
	m.mu.Unlock()
	if val == nil {
		val = 123.45
	}
	vals := make([]core.TagValue, len(tags))
	for i, tag := range tags {
		vals[i] = core.TagValue{Tag: tag, Value: val, Type: core.TypeFloat64, Quality: core.QualityGood, Timestamp: time.Now()}
	}
	return vals, nil
}
func (m *mockDriverWithState) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeCount++
	results := make([]core.WriteResult, len(commands))
	for i := range results {
		results[i] = core.WriteResult{Success: true}
	}
	return results, nil
}
func (m *mockDriverWithState) Subscribe(ctx context.Context, tags []string) (<-chan core.DataPoint, error) {
	return nil, core.ErrSubscribeNotSupported
}
func (m *mockDriverWithState) Name() string { return m.name }
func (m *mockDriverWithState) Type() string { return "mock-state" }
func (m *mockDriverWithState) Status() core.DriverStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}
func (m *mockDriverWithState) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{CanRead: true, CanWrite: true}
}

// mockTransportWithData is a mock transport that also has an OnData channel.
type mockTransportWithData struct {
	name      string
	published chan core.DataPoint
	cmdCh     chan core.WriteCommand
	dataCh    chan core.DataPoint
	status    core.TransportStatus
}

func (m *mockTransportWithData) Init(ctx context.Context, config core.TransportConfig) error {
	m.name = config.Name
	m.published = make(chan core.DataPoint, 100)
	m.cmdCh = make(chan core.WriteCommand, 10)
	m.dataCh = make(chan core.DataPoint, 100)
	m.status = core.TransportStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	return nil
}
func (m *mockTransportWithData) Start(ctx context.Context) error { return nil }
func (m *mockTransportWithData) Stop() error                     { return nil }
func (m *mockTransportWithData) Publish(ctx context.Context, point core.DataPoint) error {
	m.published <- point
	return nil
}
func (m *mockTransportWithData) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	for i := range points {
		m.published <- points[i]
	}
	return nil
}
func (m *mockTransportWithData) OnCommand() <-chan core.WriteCommand { return m.cmdCh }
func (m *mockTransportWithData) OnData() <-chan core.DataPoint       { return m.dataCh }
func (m *mockTransportWithData) Name() string                        { return m.name }
func (m *mockTransportWithData) Type() string                        { return "mock-data" }
func (m *mockTransportWithData) Status() core.TransportStatus        { return m.status }

func registerMockTypes() {
	core.RegisterDriver("mock-state", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriverWithState{readVal: 123.45}, nil
	})
	core.RegisterTransport("mock-data", func(config core.TransportConfig) (core.Transport, error) {
		return &mockTransportWithData{}, nil
	})
}

// --- Tests ---

func TestEngineSuspendResume(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		Rules:      []core.RuleConfig{{Name: "fwd", Match: "ALL", Action: "forward", Target: "t1"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if eng.Status() != core.EngineStatusRunning {
		t.Errorf("expected running, got %v", eng.Status())
	}

	if err := eng.Suspend(); err != nil {
		t.Fatalf("Suspend failed: %v", err)
	}
	if eng.Status() != core.EngineStatusSuspended {
		t.Errorf("expected suspended, got %v", eng.Status())
	}

	if err := eng.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if eng.Status() != core.EngineStatusRunning {
		t.Errorf("expected running after resume, got %v", eng.Status())
	}

	eng.Stop()
}

func TestEngineRemoveDriver(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
			{Name: "d2", Type: "mock-state", Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Verify both drivers exist.
	if _, ok := eng.GetDriver("d1"); !ok {
		t.Error("expected d1 to exist")
	}
	if _, ok := eng.GetDriver("d2"); !ok {
		t.Error("expected d2 to exist")
	}

	// Remove d1.
	if err := eng.RemoveDriver("d1"); err != nil {
		t.Fatalf("RemoveDriver failed: %v", err)
	}
	if _, ok := eng.GetDriver("d1"); ok {
		t.Error("expected d1 to be removed")
	}

	// Remove non-existent driver.
	if err := eng.RemoveDriver("nonexistent"); err == nil {
		t.Error("expected error removing non-existent driver")
	}

	eng.Stop()
}

func TestEngineRemoveTransport(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mock-data"},
			{Name: "t2", Type: "mock-data"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Remove t1.
	if err := eng.RemoveTransport("t1"); err != nil {
		t.Fatalf("RemoveTransport failed: %v", err)
	}
	if _, ok := eng.GetTransport("t1"); ok {
		t.Error("expected t1 to be removed")
	}

	// Remove non-existent transport.
	if err := eng.RemoveTransport("nonexistent"); err == nil {
		t.Error("expected error removing non-existent transport")
	}

	eng.Stop()
}

func TestEngineListDriversTransports(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
			{Name: "d2", Type: "mock-state", Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mock-data"},
			{Name: "t2", Type: "mock-data"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	drivers := eng.ListDrivers()
	if len(drivers) != 2 {
		t.Errorf("expected 2 drivers, got %d", len(drivers))
	}

	transports := eng.ListTransports()
	if len(transports) != 2 {
		t.Errorf("expected 2 transports, got %d", len(transports))
	}
}

func TestEngineSubscribe(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "50ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Subscribe to data points.
	ch, unsub := eng.Subscribe("")
	defer unsub()

	// Wait for some data.
	select {
	case p := <-ch:
		if p.Driver != "d1" {
			t.Errorf("expected driver d1, got %s", p.Driver)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for subscribed data")
	}

	// Test SubscribeWithBuffer (method on concrete type, not interface).
	ce := eng.(*CoreCEngine)
	ch2, unsub2 := ce.SubscribeWithBuffer("", 10)
	defer unsub2()

	select {
	case <-ch2:
		// got data
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for buffered subscription")
	}
}

func TestEngineOnAlert(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "50ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		Rules: []core.RuleConfig{
			{Name: "alert-all", Match: "ALL", Action: "alert", Target: "t1"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	alertReceived := make(chan core.DataPoint, 10)
	eng.OnAlert(func(point core.DataPoint, rule core.Rule) {
		alertReceived <- point
	})

	select {
	case p := <-alertReceived:
		if p.Driver != "d1" {
			t.Errorf("expected alert from d1, got %s", p.Driver)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for alert")
	}
}

func TestEngineRuleDrop(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "50ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		Rules: []core.RuleConfig{
			{Name: "drop-all", Match: "ALL", Action: "drop", Priority: 1},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for at least one scheduler tick (proving the drop rule was
	// applied), then verify no data reached the transport.
	if !waitFor(2*time.Second, func() bool {
		return eng.Stats().TotalRead > 0
	}) {
		t.Fatal("timed out waiting for scheduler tick")
	}

	tr, ok := eng.GetTransport("t1")
	if !ok {
		t.Fatal("transport not found")
	}
	mt := tr.(*mockTransportWithData)

	// Channel should be empty (no published data).
	select {
	case p := <-mt.published:
		t.Errorf("expected no published data with drop rule, got: %+v", p)
	default:
		// good — no data published
	}

	eng.Stop()
}

func TestEngineRuleTransform(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "50ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		Rules: []core.RuleConfig{
			{
				Name:     "transform-val",
				Match:    "ALL",
				Action:   "transform",
				Target:   "t1",
				Priority: 1,
				Transform: &core.TransformConfig{
					Expression: "value * 2",
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	tr, _ := eng.GetTransport("t1")
	mt := tr.(*mockTransportWithData)

	select {
	case p := <-mt.published:
		// Original value is 123.45, transform is *2 → 246.9
		val, ok := p.Value.(float64)
		if !ok {
			t.Fatalf("expected float64, got %T: %v", p.Value, p.Value)
		}
		if val < 240 || val > 250 {
			t.Errorf("expected transformed value ~246.9, got %v", val)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for transformed data")
	}
}

func TestEngineCommandFlow(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Send a write command through the transport's command channel.
	tr, _ := eng.GetTransport("t1")
	mt := tr.(*mockTransportWithData)

	mt.cmdCh <- core.WriteCommand{Driver: "d1", Tag: "t1", Value: 42.0}

	// Wait for the command listener to process the write.
	d, _ := eng.GetDriver("d1")
	md := d.(*mockDriverWithState)
	if !waitFor(2*time.Second, func() bool {
		md.mu.Lock()
		defer md.mu.Unlock()
		return md.writeCount > 0
	}) {
		t.Fatal("timed out waiting for write command to be processed")
	}

	// Verify the driver received the write.
	writes := func() int {
		md.mu.Lock()
		defer md.mu.Unlock()
		return md.writeCount
	}()
	if writes == 0 {
		t.Error("expected at least 1 write to the driver")
	}
}

func TestEngineDataListenerFlow(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{},
		Transports: []core.TransportConfig{
			{Name: "relay", Type: "mock-data"},
		},
		Rules: []core.RuleConfig{
			{Name: "fwd", Match: "ALL", Action: "forward", Target: "relay"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// The relay transport has an OnData channel. Push a data point into it.
	tr, _ := eng.GetTransport("relay")
	mt := tr.(*mockTransportWithData)

	// Send data through the transport's data channel (simulating inbound data).
	mt.dataCh <- core.DataPoint{
		Driver:    "upstream",
		Tag:       "sensor1",
		Value:     99.9,
		Timestamp: time.Now(),
	}

	// The engine should pick this up and forward it back to the transport's publish channel.
	select {
	case p := <-mt.published:
		if p.Tag != "sensor1" || p.Value != 99.9 {
			t.Errorf("unexpected published point: %+v", p)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for data listener to forward inbound data")
	}
}

func TestEngineReadTagNotFound(t *testing.T) {
	eng := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Read from non-existent driver.
	_, err := eng.ReadTag(ctx, "nonexistent", "t1")
	if err == nil {
		t.Error("expected error reading from non-existent driver")
	}
}

func TestEngineWriteTagNotFound(t *testing.T) {
	eng := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Write to non-existent driver.
	_, err := eng.WriteTag(ctx, core.WriteCommand{Driver: "nonexistent", Tag: "t1", Value: 1})
	if err == nil {
		t.Error("expected error writing to non-existent driver")
	}
}

func TestEngineLatestValues(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "50ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		Rules:      []core.RuleConfig{{Name: "fwd", Match: "ALL", Action: "forward", Target: "t1"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for data to flow into the cache.
	if !waitFor(2*time.Second, func() bool {
		return len(eng.LatestValues("d1")) > 0
	}) {
		t.Fatal("timed out waiting for cached values")
	}

	values := eng.LatestValues("d1")
	if len(values) == 0 {
		t.Error("expected at least one cached value for d1")
	}
	if v, ok := values["t1"]; !ok || v.Driver != "d1" {
		t.Errorf("expected cached value for d1/t1, got: %+v", values)
	}

	eng.Stop()
}

func TestEngineStats(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "50ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		Rules:      []core.RuleConfig{{Name: "fwd", Match: "ALL", Action: "forward", Target: "t1"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for some data to flow.
	if !waitFor(2*time.Second, func() bool {
		return eng.Stats().TotalRead > 0
	}) {
		t.Fatal("timed out waiting for reads")
	}

	stats := eng.Stats()
	if stats.Drivers != 1 {
		t.Errorf("expected 1 driver, got %d", stats.Drivers)
	}
	if stats.Transports != 1 {
		t.Errorf("expected 1 transport, got %d", stats.Transports)
	}
	if stats.TotalRead == 0 {
		t.Error("expected non-zero total reads")
	}
	if stats.TotalPublish == 0 {
		t.Error("expected non-zero total publishes")
	}

	eng.Stop()
}

func TestEngineRuleStats(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "50ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		Rules:      []core.RuleConfig{{Name: "fwd", Match: "ALL", Action: "forward", Target: "t1"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	if !waitFor(2*time.Second, func() bool {
		rs := eng.GetRuleStats()
		return len(rs) == 1 && rs[0].HitCount > 0
	}) {
		t.Fatal("timed out waiting for rule hit")
	}

	ruleStats := eng.GetRuleStats()
	if len(ruleStats) != 1 {
		t.Fatalf("expected 1 rule stat, got %d", len(ruleStats))
	}
	if ruleStats[0].HitCount == 0 {
		t.Error("expected non-zero hit count")
	}
}

func TestEngineSetRuleDisabled(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "50ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
		Rules:      []core.RuleConfig{{Name: "fwd", Match: "ALL", Action: "forward", Target: "t1"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Disable rule 0.
	if err := eng.SetRuleDisabled(0, true); err != nil {
		t.Fatalf("SetRuleDisabled failed: %v", err)
	}

	// Verify disabled state.
	stats := eng.GetRuleStats()
	if !stats[0].Disabled {
		t.Error("expected rule to be disabled")
	}

	// Re-enable.
	if err := eng.SetRuleDisabled(0, false); err != nil {
		t.Fatalf("SetRuleDisabled failed: %v", err)
	}

	// Out of range.
	if err := eng.SetRuleDisabled(99, true); err == nil {
		t.Error("expected error for out-of-range index")
	}
}

func TestEngineReload(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg1 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg1); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Reload with a different config.
	cfg2 := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d2", Type: "mock-state", Tags: []core.TagConfig{{Name: "t2", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Reload(cfg2); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// d2 should now exist after reload.
	if _, ok := eng.GetDriver("d2"); !ok {
		t.Error("expected d2 to exist after reload")
	}
	// d1 should NOT exist — Reload must clear stale drivers.
	if _, ok := eng.GetDriver("d1"); ok {
		t.Error("expected d1 to be removed after reload")
	}
}

func TestEngineEmptyConfig(t *testing.T) {
	eng := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Engine with no drivers, no transports, no rules — relay mode with no inbound.
	// This should fail validation or start successfully with no work to do.
	cfg := &core.Config{
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start with empty config failed: %v", err)
	}

	stats := eng.Stats()
	if stats.Drivers != 0 || stats.Transports != 1 {
		t.Errorf("unexpected stats: %+v", stats)
	}

	eng.Stop()
}

func TestEngineDoubleStop(t *testing.T) {
	registerMockTypes()

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{Name: "d1", Type: "mock-state", Tags: []core.TagConfig{{Name: "t1", Interval: "100ms"}}},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := eng.Stop(); err != nil {
		t.Fatalf("first Stop failed: %v", err)
	}

	// Second stop should not panic.
	// (It may return nil or an error, but must not crash.)
	_ = eng.Stop()
}
