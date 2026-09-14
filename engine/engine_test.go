package engine

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// waitFor polls check until it returns true or timeout expires.
func waitFor(timeout time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestDataBus(t *testing.T) {
	bus := NewDataBus(10)
	defer bus.Close()

	// Subscribe with a filter matching the DataPoint's Driver field.
	// (Broadcast only delivers to subscribers whose filter is "" or equals
	// the point's Driver.)
	subCh, unsub := bus.Subscribe("test-driver")
	defer unsub()

	dp := core.DataPoint{
		Driver:    "test-driver",
		Tag:       "temp",
		Value:     25.5,
		Timestamp: time.Now(),
	}

	bus.Broadcast(dp)

	select {
	case p := <-subCh:
		if p.Tag != "temp" || p.Value != 25.5 {
			t.Errorf("unexpected point received: %+v", p)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for subscriber message")
	}

	bus.Push(dp)
	select {
	case p := <-bus.Channel():
		if p.Tag != "temp" {
			t.Errorf("unexpected point from channel: %+v", p)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for bus channel")
	}
}

func TestLatestCache(t *testing.T) {
	cache := NewLatestCache()
	dp := core.DataPoint{
		Driver:    "driver1",
		Tag:       "pressure",
		Value:     101.3,
		Timestamp: time.Now(),
	}

	cache.Update(dp)

	val, ok := cache.Get("driver1", "pressure")
	if !ok || val.Value != 101.3 {
		t.Fatalf("cache.Get failed: ok=%v, val=%+v", ok, val)
	}

	_, ok = cache.Get("driver1", "nonexistent")
	if ok {
		t.Error("expected false for nonexistent tag")
	}

	all := cache.GetByDriver("driver1")
	if len(all) != 1 || all["pressure"].Value != 101.3 {
		t.Fatalf("unexpected GetByDriver result: %+v", all)
	}

	cache.Clear()
	if len(cache.GetByDriver("driver1")) != 0 {
		t.Fatal("expected cache to be empty after clear")
	}
}

func TestScheduler(t *testing.T) {
	received := make(chan []core.TagValue, 10)
	sched := NewScheduler(
		func(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
			return []core.TagValue{
				{Tag: tags[0], Value: 42, Timestamp: time.Now()},
			}, nil
		},
		func(driver string, values []core.TagValue) {
			received <- values
		},
		0, // default error-throttle window
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("scheduler start failed: %v", err)
	}
	defer sched.Stop()

	err := sched.AddTask(core.ScheduleTask{
		ID:       "task-1",
		Driver:   "d1",
		Tags:     []string{"tagA"},
		Interval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	select {
	case vals := <-received:
		if len(vals) != 1 || vals[0].Value != 42 {
			t.Errorf("unexpected values received: %+v", vals)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for scheduled task execution")
	}

	stats := sched.Stats()
	if stats.ActiveTasks != 1 {
		t.Errorf("expected 1 active task, got %d", stats.ActiveTasks)
	}

	if err := sched.PauseDriver("d1"); err != nil {
		t.Fatalf("PauseDriver failed: %v", err)
	}
	stats = sched.Stats()
	if stats.PausedTasks != 1 {
		t.Errorf("expected 1 paused task, got %d", stats.PausedTasks)
	}

	if err := sched.ResumeDriver("d1"); err != nil {
		t.Fatalf("ResumeDriver failed: %v", err)
	}

	if err := sched.RemoveTask("task-1"); err != nil {
		t.Fatalf("RemoveTask failed: %v", err)
	}
}

type mockDriver struct {
	name   string
	status core.DriverStatus
}

func (m *mockDriver) Init(ctx context.Context, config core.DriverConfig) error {
	m.name = config.Name
	m.status = core.DriverStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	return nil
}
func (m *mockDriver) Start(ctx context.Context) error                             { return nil }
func (m *mockDriver) Stop() error                                                 { return nil }
func (m *mockDriver) Restart(ctx context.Context, config core.DriverConfig) error { return nil }
func (m *mockDriver) Read(ctx context.Context, tags []string) ([]core.TagValue, error) {
	vals := make([]core.TagValue, len(tags))
	for i, tag := range tags {
		vals[i] = core.TagValue{Tag: tag, Value: 123.45, Type: core.TypeFloat64, Quality: core.QualityGood, Timestamp: time.Now()}
	}
	return vals, nil
}
func (m *mockDriver) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	return []core.WriteResult{{Success: true}}, nil
}
func (m *mockDriver) Subscribe(ctx context.Context, tags []string) (<-chan core.DataPoint, error) {
	return nil, core.ErrSubscribeNotSupported
}
func (m *mockDriver) Name() string              { return m.name }
func (m *mockDriver) Type() string              { return "mock" }
func (m *mockDriver) Status() core.DriverStatus { return m.status }
func (m *mockDriver) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{CanRead: true, CanWrite: true}
}

type mockTransport struct {
	name      string
	published chan core.DataPoint
	cmdCh     chan core.WriteCommand
	status    core.TransportStatus
}

func (m *mockTransport) Init(ctx context.Context, config core.TransportConfig) error {
	m.name = config.Name
	m.published = make(chan core.DataPoint, 100)
	m.cmdCh = make(chan core.WriteCommand, 10)
	m.status = core.TransportStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	return nil
}
func (m *mockTransport) Start(ctx context.Context) error { return nil }
func (m *mockTransport) Stop() error                     { return nil }
func (m *mockTransport) Publish(ctx context.Context, point core.DataPoint) error {
	m.published <- point
	return nil
}
func (m *mockTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	for i := range points {
		m.published <- points[i]
	}
	return nil
}
func (m *mockTransport) OnCommand() <-chan core.WriteCommand { return m.cmdCh }
func (m *mockTransport) OnData() <-chan core.DataPoint       { return nil }
func (m *mockTransport) Name() string                        { return m.name }
func (m *mockTransport) Type() string                        { return "mock" }
func (m *mockTransport) Status() core.TransportStatus        { return m.status }

func TestEngineLifecycle(t *testing.T) {
	core.RegisterDriver("mock-driver", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})
	core.RegisterTransport("mock-transport", func(config core.TransportConfig) (core.Transport, error) {
		return &mockTransport{}, nil
	})

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{
				Name: "d1",
				Type: "mock-driver",
				Tags: []core.TagConfig{
					{Name: "temp", Interval: "50ms"},
				},
			},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mock-transport"},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "t1"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Engine.Start failed: %v", err)
	}

	// Read a tag
	val, err := eng.ReadTag(ctx, "d1", "temp")
	if err != nil || val.Value != 123.45 {
		t.Fatalf("ReadTag failed: val=%+v, err=%v", val, err)
	}

	// Write a tag
	wRes, err := eng.WriteTag(ctx, core.WriteCommand{Driver: "d1", Tag: "temp", Value: 99.0})
	if err != nil || !wRes.Success {
		t.Fatalf("WriteTag failed: res=%+v, err=%v", wRes, err)
	}

	// The select below already waits up to 500ms for data, so no sleep needed.
	tr, ok := eng.GetTransport("t1")
	if !ok {
		t.Fatal("transport t1 not found")
	}
	mt := tr.(*mockTransport)

	select {
	case p := <-mt.published:
		if p.Tag != "temp" {
			t.Errorf("unexpected published point: %+v", p)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for transport published data")
	}

	stats := eng.Stats()
	if stats.Drivers != 1 || stats.Transports != 1 || stats.Rules != 1 {
		t.Errorf("unexpected engine stats: %+v", stats)
	}

	if err := eng.Stop(); err != nil {
		t.Fatalf("Engine.Stop failed: %v", err)
	}
}

// TestGroupEnrichment verifies that DataPoint.Group is populated from
// TagConfig.Group when data flows through the scheduler -> onDriverData path.
// Before the fix, onDriverData only set Driver and Group was always empty,
// breaking rules like "group == 'reactor'" and MQTT topic templates.
func TestGroupEnrichment(t *testing.T) {
	core.RegisterDriver("mock-driver", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})
	core.RegisterTransport("mock-transport", func(config core.TransportConfig) (core.Transport, error) {
		return &mockTransport{}, nil
	})

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{
				Name: "d1",
				Type: "mock-driver",
				Tags: []core.TagConfig{
					{Name: "temp", Interval: "50ms", Group: "sensors"},
					{Name: "pressure", Interval: "50ms"}, // no group -> Group must stay empty
				},
			},
		},
		Transports: []core.TransportConfig{
			{Name: "t1", Type: "mock-transport"},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "t1"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Engine.Start failed: %v", err)
	}

	// Wait for at least one scheduled tick to be collected before stopping.
	if !waitFor(2*time.Second, func() bool {
		return len(eng.LatestValues("d1")) > 0
	}) {
		t.Fatal("timed out waiting for scheduler to collect data")
	}

	// Stop the engine before draining.  The scheduler publishes every
	// 50 ms; if left running the drain loop's 100 ms timeout would
	// never fire (a new point always arrives first), causing an
	// infinite loop / test timeout.
	if err := eng.Stop(); err != nil {
		t.Fatalf("Engine.Stop failed: %v", err)
	}

	tr, ok := eng.GetTransport("t1")
	if !ok {
		t.Fatal("transport t1 not found")
	}
	mt := tr.(*mockTransport)

	sawTempGroup := false
	sawPressureNoGroup := false
	// Drain whatever has been published so far.
drain:
	for {
		select {
		case p := <-mt.published:
			switch p.Tag {
			case "temp":
				if p.Group == "sensors" && p.Driver == "d1" {
					sawTempGroup = true
				}
			case "pressure":
				if p.Group == "" && p.Driver == "d1" {
					sawPressureNoGroup = true
				}
			}
		case <-time.After(100 * time.Millisecond):
			break drain
		}
	}
	if !sawTempGroup {
		t.Error("expected a published point with tag=temp and group=sensors, none seen")
	}
	if !sawPressureNoGroup {
		t.Error("expected a published point with tag=pressure and empty group, none seen")
	}
}
