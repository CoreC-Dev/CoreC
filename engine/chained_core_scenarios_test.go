package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/transport/parser"
)

// ============================================================
// Enhanced mock transport for scenario testing.
// Supports parser integration (simulating MQTT/HTTP inbound parsing),
// larger publish buffers, and write-command recording.
// ============================================================

// scenarioTransport simulates a full chained-core transport:
//   - inbound: raw payloads arrive via FeedRaw(), are parsed through
//     the configured parser, and pushed to the OnData channel
//     (exactly like MQTT subscribeData or HTTP handleWebhook).
//   - outbound: Publish() captures published DataPoints.
//   - commands: OnCommand() channel for downstream write commands.
type scenarioTransport struct {
	name     string
	status   core.TransportStatus
	dataCh   chan core.DataPoint
	cmdCh    chan core.WriteCommand
	publish  chan core.DataPoint
	pubCount atomic.Int64
	p        parser.Parser // optional parser for inbound payloads
}

func newScenarioTransport(name string, bufSize int) *scenarioTransport {
	if bufSize <= 0 {
		bufSize = 256
	}
	return &scenarioTransport{
		name:    name,
		dataCh:  make(chan core.DataPoint, bufSize),
		cmdCh:   make(chan core.WriteCommand, bufSize),
		publish: make(chan core.DataPoint, bufSize),
	}
}

func (t *scenarioTransport) Init(ctx context.Context, config core.TransportConfig) error {
	t.name = config.Name
	t.status = core.TransportStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	// Create parser from settings if configured.
	if p, err := parser.New(config.Settings); err == nil {
		t.p = p
	}
	return nil
}
func (t *scenarioTransport) Start(ctx context.Context) error { return nil }
func (t *scenarioTransport) Stop() error                     { return nil }
func (t *scenarioTransport) Publish(ctx context.Context, point core.DataPoint) error {
	t.pubCount.Add(1)
	select {
	case t.publish <- point:
	default:
	}
	return nil
}
func (t *scenarioTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	for i := range points {
		_ = t.Publish(ctx, points[i])
	}
	return nil
}
func (t *scenarioTransport) OnCommand() <-chan core.WriteCommand { return t.cmdCh }
func (t *scenarioTransport) OnData() <-chan core.DataPoint       { return t.dataCh }
func (t *scenarioTransport) Name() string                        { return t.name }
func (t *scenarioTransport) Type() string                        { return "scenario" }
func (t *scenarioTransport) Status() core.TransportStatus        { return t.status }

// FeedRaw simulates an upstream MQTT message or HTTP webhook body:
// it parses the raw payload through the transport's parser and pushes
// the resulting DataPoint into the OnData channel — exactly what
// MQTT subscribeData and HTTP handleWebhook do in production.
func (t *scenarioTransport) FeedRaw(payload []byte, topic string) error {
	if t.p == nil {
		return fmt.Errorf("no parser configured")
	}
	dp, err := t.p.Parse(payload, topic)
	if err != nil {
		return err
	}
	t.dataCh <- dp
	return nil
}

// FeedData pushes a pre-parsed DataPoint directly (simulating a
// CoreC→CoreC chain where no parser is needed).
func (t *scenarioTransport) FeedData(dp core.DataPoint) {
	t.dataCh <- dp
}

// FeedCommand simulates a downstream command arriving via MQTT
// command-topic or HTTP command endpoint.
func (t *scenarioTransport) FeedCommand(cmd core.WriteCommand) {
	t.cmdCh <- cmd
}

// DrainPublished collects all published points within a timeout.
func (t *scenarioTransport) DrainPublished(timeout time.Duration) []core.DataPoint {
	var result []core.DataPoint
	deadline := time.After(timeout)
	for {
		select {
		case p := <-t.publish:
			result = append(result, p)
		case <-deadline:
			return result
		}
	}
}

// ============================================================
// Helper: create and start an engine with given config.
// ============================================================

func startTestEngine(t *testing.T, cfg *core.Config) (core.Engine, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	eng := New()
	if err := eng.Start(ctx, cfg); err != nil {
		cancel()
		t.Fatalf("engine Start: %v", err)
	}
	return eng, cancel
}

// dummyDriverConfig returns a minimal driver config required by the engine.
func dummyDriverConfig(typeName string) core.DriverConfig {
	return core.DriverConfig{
		Name:     "dummy",
		Type:     typeName,
		Settings: map[string]any{},
		Tags:     []core.TagConfig{},
	}
}

// ============================================================
// Scenario ①: 纯订阅转发（协议网关）
// CoreC 不连任何设备，只订阅第三方设备的 MQTT，解析后转发到云端。
// 关键：parser: jsonpath 将第三方 JSON 转换为 DataPoint。
// ============================================================

func TestScenario1_PureSubscribeForward(t *testing.T) {
	const transportType = "scenario-1"
	var st *scenarioTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		st = newScenarioTransport(config.Name, 256)
		return st, nil
	})
	core.RegisterDriver("mock-d-s1", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-s1")},
		Transports: []core.TransportConfig{
			{
				Name: "bridge",
				Type: transportType,
				Settings: map[string]any{
					"parser": map[string]any{
						"type":      "jsonpath",
						"driver":    "lora",
						"tag":       "{{ .payload.dev_id }}",
						"value":     "{{ .payload.temp }}",
						"data-type": "float32",
					},
				},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "bridge"},
		},
	}

	eng, cancel := startTestEngine(t, cfg)
	defer cancel()
	defer eng.Stop()

	// Simulate third-party LoRa gateway publishing JSON to MQTT topic "lora/sensor-01/up".
	thirdPartyPayload := []byte(`{"dev_id":"sensor-01","temp":23.5,"humidity":60}`)
	if err := st.FeedRaw(thirdPartyPayload, "lora/sensor-01/up"); err != nil {
		t.Fatalf("FeedRaw: %v", err)
	}

	published := st.DrainPublished(2 * time.Second)
	if len(published) != 1 {
		t.Fatalf("expected 1 published point, got %d", len(published))
	}
	p := published[0]
	if p.Driver != "lora" {
		t.Errorf("driver: expected lora, got %s", p.Driver)
	}
	if p.Tag != "sensor-01" {
		t.Errorf("tag: expected sensor-01, got %s", p.Tag)
	}
	if v, ok := p.Value.(float64); !ok || v != 23.5 {
		t.Errorf("value: expected 23.5, got %v", p.Value)
	}
	if p.Type != core.TypeFloat32 {
		t.Errorf("type: expected float32, got %s", p.Type)
	}
}

// ============================================================
// Scenario ②: 两个 CoreC 级联（边缘 → 云端）
// A 在边缘采集，B 在云端收数据做告警/转发。
// 关键：parser: default (json.Unmarshal)，规则匹配 alert + forward。
// ============================================================

func TestScenario2_TwoCoreCCascade(t *testing.T) {
	const transportType = "scenario-2"
	var st *scenarioTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		st = newScenarioTransport(config.Name, 256)
		return st, nil
	})
	core.RegisterDriver("mock-d-s2", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-s2")},
		Transports: []core.TransportConfig{
			{
				Name: "mqtt-chain",
				Type: transportType,
				Settings: map[string]any{
					"parser": map[string]any{"type": "default"},
				},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "alert-high-temp", Match: "tag == 'temperature' && value > 90", Action: "alert", Target: "mqtt-chain", Priority: 1},
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "mqtt-chain", Priority: 999},
		},
	}

	eng, cancel := startTestEngine(t, cfg)
	defer cancel()
	defer eng.Stop()

	// Register alert handler.
	alertFired := make(chan core.DataPoint, 1)
	eng.OnAlert(func(point core.DataPoint, rule core.Rule) {
		select {
		case alertFired <- point:
		default:
		}
	})

	// Simulate CoreC-A sending a normal DataPoint (via default parser = json.Unmarshal).
	normalDP := core.DataPoint{
		Driver:    "plc",
		Tag:       "temperature",
		Value:     75.0,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}
	payload, _ := json.Marshal(normalDP)
	if err := st.FeedRaw(payload, "edgeA/plc/temperature"); err != nil {
		t.Fatalf("FeedRaw: %v", err)
	}

	published := st.DrainPublished(2 * time.Second)
	if len(published) != 1 {
		t.Fatalf("normal: expected 1 published, got %d", len(published))
	}
	if published[0].Value != 75.0 {
		t.Errorf("normal: expected value 75.0, got %v", published[0].Value)
	}
	// No alert for 75.0
	select {
	case <-alertFired:
		t.Error("unexpected alert for normal temperature")
	case <-time.After(100 * time.Millisecond):
	}

	// Simulate CoreC-A sending a high-temperature DataPoint.
	highDP := core.DataPoint{
		Driver:    "plc",
		Tag:       "temperature",
		Value:     95.0,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}
	payload2, _ := json.Marshal(highDP)
	if err := st.FeedRaw(payload2, "edgeA/plc/temperature"); err != nil {
		t.Fatalf("FeedRaw: %v", err)
	}

	// Alert should fire.
	select {
	case alertPoint := <-alertFired:
		if alertPoint.Value != 95.0 {
			t.Errorf("alert: expected value 95.0, got %v", alertPoint.Value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: alert not fired for high temperature")
	}

	// Data should also be forwarded.
	published2 := st.DrainPublished(2 * time.Second)
	if len(published2) != 1 {
		t.Fatalf("high: expected 1 published, got %d", len(published2))
	}
	if published2[0].Value != 95.0 {
		t.Errorf("high: expected value 95.0, got %v", published2[0].Value)
	}
}

// ============================================================
// Scenario ③: 多级级联（边缘 → 网关 → 云端）
// 三跳：A → B → C，每一跳都可以做规则处理。
// 关键：多个 engine 级联，每跳处理后再转发。
// ============================================================

func TestScenario3_MultiHopCascade(t *testing.T) {
	const transportType = "scenario-3"
	var transports []*scenarioTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		st := newScenarioTransport(config.Name, 256)
		transports = append(transports, st)
		return st, nil
	})
	core.RegisterDriver("mock-d-s3", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	// Engine B (gateway): receives from A, adds group, forwards to C.
	cfgB := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-s3")},
		Transports: []core.TransportConfig{
			{
				Name: "from-edge",
				Type: transportType,
				Settings: map[string]any{
					"parser": map[string]any{"type": "default"},
				},
			},
			{
				Name:     "to-cloud",
				Type:     transportType,
				Settings: map[string]any{},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-to-cloud", Match: "ALL", Action: "forward", Target: "to-cloud"},
		},
	}

	engB, cancelB := startTestEngine(t, cfgB)
	defer cancelB()
	defer engB.Stop()

	if len(transports) < 2 {
		t.Fatalf("expected at least 2 transports, got %d", len(transports))
	}
	fromEdge := transports[0] // receives from A
	toCloud := transports[1]  // publishes to C

	// Simulate CoreC-A sending data.
	originalDP := core.DataPoint{
		Driver:    "plc",
		Tag:       "temperature",
		Value:     42.0,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}
	payload, _ := json.Marshal(originalDP)
	if err := fromEdge.FeedRaw(payload, "edgeA/plc/temperature"); err != nil {
		t.Fatalf("FeedRaw: %v", err)
	}

	// B should forward to to-cloud transport.
	publishedAtB := toCloud.DrainPublished(2 * time.Second)
	if len(publishedAtB) != 1 {
		t.Fatalf("hop B: expected 1 published, got %d", len(publishedAtB))
	}
	if publishedAtB[0].Value != 42.0 {
		t.Errorf("hop B: expected value 42.0, got %v", publishedAtB[0].Value)
	}

	// Now simulate hop C receiving the data from B.
	// Create engine C.
	transports = nil // reset for engine C
	cfgC := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-s3")},
		Transports: []core.TransportConfig{
			{
				Name: "from-gateway",
				Type: transportType,
				Settings: map[string]any{
					"parser": map[string]any{"type": "default"},
				},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "from-gateway"},
		},
	}

	engC, cancelC := startTestEngine(t, cfgC)
	defer cancelC()
	defer engC.Stop()

	if len(transports) < 1 {
		t.Fatalf("expected transport for engine C, got %d", len(transports))
	}
	fromGateway := transports[0]

	// Feed B's output into C's input.
	payloadFromB, _ := json.Marshal(publishedAtB[0])
	if err := fromGateway.FeedRaw(payloadFromB, "gateway/plc/temperature"); err != nil {
		t.Fatalf("FeedRaw to C: %v", err)
	}

	publishedAtC := fromGateway.DrainPublished(2 * time.Second)
	if len(publishedAtC) != 1 {
		t.Fatalf("hop C: expected 1 published, got %d", len(publishedAtC))
	}
	if publishedAtC[0].Value != 42.0 {
		t.Errorf("hop C: expected value 42.0, got %v", publishedAtC[0].Value)
	}
	if publishedAtC[0].Tag != "temperature" {
		t.Errorf("hop C: expected tag temperature, got %s", publishedAtC[0].Tag)
	}
}

// ============================================================
// Scenario ④: 协议转换（MQTT → HTTP）
// CoreC 从 MQTT 收数据，转发到 HTTP endpoint。
// 关键：一个 transport 收（OnData），另一个 transport 发（Publish）。
// ============================================================

func TestScenario4_ProtocolConversion(t *testing.T) {
	const inType = "scenario-4-in"   // simulates MQTT inbound
	const outType = "scenario-4-out" // simulates HTTP outbound
	var inTransport, outTransport *scenarioTransport

	core.RegisterTransport(inType, func(config core.TransportConfig) (core.Transport, error) {
		inTransport = newScenarioTransport(config.Name, 256)
		return inTransport, nil
	})
	core.RegisterTransport(outType, func(config core.TransportConfig) (core.Transport, error) {
		outTransport = newScenarioTransport(config.Name, 256)
		return outTransport, nil
	})
	core.RegisterDriver("mock-d-s4", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-s4")},
		Transports: []core.TransportConfig{
			{
				Name: "mqtt-in",
				Type: inType,
				Settings: map[string]any{
					"parser": map[string]any{"type": "default"},
				},
			},
			{
				Name:     "http-out",
				Type:     outType,
				Settings: map[string]any{},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "mqtt-to-http", Match: "ALL", Action: "forward", Target: "http-out"},
		},
	}

	eng, cancel := startTestEngine(t, cfg)
	defer cancel()
	defer eng.Stop()

	// Data arrives via MQTT (simulated).
	dp := core.DataPoint{
		Driver:    "sensor",
		Tag:       "humidity",
		Value:     65.0,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}
	payload, _ := json.Marshal(dp)
	if err := inTransport.FeedRaw(payload, "edge/sensor/humidity"); err != nil {
		t.Fatalf("FeedRaw: %v", err)
	}

	// Data should be forwarded to HTTP outbound transport.
	published := outTransport.DrainPublished(2 * time.Second)
	if len(published) != 1 {
		t.Fatalf("expected 1 published to HTTP, got %d", len(published))
	}
	if published[0].Tag != "humidity" {
		t.Errorf("expected tag humidity, got %s", published[0].Tag)
	}
	if published[0].Value != 65.0 {
		t.Errorf("expected value 65.0, got %v", published[0].Value)
	}

	// MQTT inbound transport should NOT receive its own forwarded data.
	inPublished := inTransport.DrainPublished(200 * time.Millisecond)
	if len(inPublished) != 0 {
		t.Errorf("MQTT inbound should not receive published data, got %d", len(inPublished))
	}
}

// ============================================================
// Scenario ⑤: 多对一汇聚
// 多个边缘节点 → 一个汇聚节点。
// 关键：多个 transport 的 OnData 汇入同一条 DataBus。
// ============================================================

func TestScenario5_ManyToOneAggregation(t *testing.T) {
	const transportType = "scenario-5"
	var transports []*scenarioTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		st := newScenarioTransport(config.Name, 256)
		transports = append(transports, st)
		return st, nil
	})
	core.RegisterDriver("mock-d-s5", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-s5")},
		Transports: []core.TransportConfig{
			{Name: "edge-a", Type: transportType, Settings: map[string]any{}},
			{Name: "edge-b", Type: transportType, Settings: map[string]any{}},
			{Name: "edge-c", Type: transportType, Settings: map[string]any{}},
			{Name: "aggregator", Type: transportType, Settings: map[string]any{}},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "aggregator"},
		},
	}

	eng, cancel := startTestEngine(t, cfg)
	defer cancel()
	defer eng.Stop()

	if len(transports) != 4 {
		t.Fatalf("expected 4 transports, got %d", len(transports))
	}
	edgeA, edgeB, edgeC, aggregator := transports[0], transports[1], transports[2], transports[3]

	// All three edges send data concurrently.
	var wg sync.WaitGroup
	for i, edge := range []*scenarioTransport{edgeA, edgeB, edgeC} {
		wg.Add(1)
		go func(idx int, tr *scenarioTransport) {
			defer wg.Done()
			tr.FeedData(core.DataPoint{
				Driver:    fmt.Sprintf("edge-%d", idx),
				Tag:       "temperature",
				Value:     float64(20 + idx),
				Type:      core.TypeFloat64,
				Quality:   core.QualityGood,
				Timestamp: time.Now(),
			})
		}(i, edge)
	}
	wg.Wait()

	// Aggregator should receive all 3 data points.
	published := aggregator.DrainPublished(2 * time.Second)
	if len(published) != 3 {
		t.Fatalf("expected 3 aggregated points, got %d", len(published))
	}

	// Verify all three edges are represented.
	drivers := make(map[string]bool)
	for _, p := range published {
		drivers[p.Driver] = true
	}
	for _, expected := range []string{"edge-0", "edge-1", "edge-2"} {
		if !drivers[expected] {
			t.Errorf("expected driver %s in aggregated data, not found", expected)
		}
	}
}

// ============================================================
// Scenario ⑥: 一对多分发
// 一个数据源 → 多个目标 transport。
// 关键：action: mirror + targets 列表。
// ============================================================

func TestScenario6_OneToManyFanout(t *testing.T) {
	const transportType = "scenario-6"
	var transports []*scenarioTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		st := newScenarioTransport(config.Name, 256)
		transports = append(transports, st)
		return st, nil
	})
	core.RegisterDriver("mock-d-s6", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-s6")},
		Transports: []core.TransportConfig{
			{Name: "source", Type: transportType, Settings: map[string]any{}},
			{Name: "target-1", Type: transportType, Settings: map[string]any{}},
			{Name: "target-2", Type: transportType, Settings: map[string]any{}},
			{Name: "target-3", Type: transportType, Settings: map[string]any{}},
		},
		Rules: []core.RuleConfig{
			{
				Name:    "fanout",
				Match:   "ALL",
				Action:  "mirror",
				Targets: []string{"target-1", "target-2", "target-3"},
			},
		},
	}

	eng, cancel := startTestEngine(t, cfg)
	defer cancel()
	defer eng.Stop()

	if len(transports) != 4 {
		t.Fatalf("expected 4 transports, got %d", len(transports))
	}
	source := transports[0]
	target1, target2, target3 := transports[1], transports[2], transports[3]

	// Send one data point from source.
	source.FeedData(core.DataPoint{
		Driver:    "plc",
		Tag:       "temperature",
		Value:     50.0,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	})

	// All three targets should receive the data point.
	for idx, target := range []*scenarioTransport{target1, target2, target3} {
		published := target.DrainPublished(2 * time.Second)
		if len(published) != 1 {
			t.Errorf("target-%d: expected 1 published, got %d", idx+1, len(published))
			continue
		}
		if published[0].Value != 50.0 {
			t.Errorf("target-%d: expected value 50.0, got %v", idx+1, published[0].Value)
		}
	}

	// Source should not receive its own data back.
	sourcePublished := source.DrainPublished(200 * time.Millisecond)
	if len(sourcePublished) != 0 {
		t.Errorf("source should not receive published data, got %d", len(sourcePublished))
	}
}

// ============================================================
// Scenario ⑦: 双向级联（数据上行 + 命令下行）
// 数据从上游通过 OnData 上行，命令通过 OnCommand 下行到驱动。
// 关键：OnData + OnCommand 同时工作，WriteTag 被正确调用。
// ============================================================

// recordingDriver records all write commands for verification.
type recordingDriver struct {
	mockDriver
	written []core.WriteCommand
	mu      sync.Mutex
}

func (d *recordingDriver) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	d.mu.Lock()
	d.written = append(d.written, commands...)
	d.mu.Unlock()
	results := make([]core.WriteResult, len(commands))
	for i := range results {
		results[i] = core.WriteResult{Success: true}
	}
	return results, nil
}

func (d *recordingDriver) GetWritten() []core.WriteCommand {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]core.WriteCommand{}, d.written...)
}

func TestScenario7_BidirectionalCascade(t *testing.T) {
	const transportType = "scenario-7"
	var st *scenarioTransport
	var rd *recordingDriver

	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		st = newScenarioTransport(config.Name, 256)
		return st, nil
	})
	core.RegisterDriver("recording-d-s7", func(config core.DriverConfig) (core.Driver, error) {
		rd = &recordingDriver{}
		rd.name = config.Name
		rd.status = core.DriverStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
		return rd, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{
				Name:     "plc",
				Type:     "recording-d-s7",
				Settings: map[string]any{},
				Tags: []core.TagConfig{
					{Name: "setpoint", Address: "40001", Type: "float32"},
				},
			},
		},
		Transports: []core.TransportConfig{
			{
				Name: "bidirectional",
				Type: transportType,
				Settings: map[string]any{
					"parser": map[string]any{"type": "default"},
				},
			},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "bidirectional"},
		},
	}

	eng, cancel := startTestEngine(t, cfg)
	defer cancel()
	defer eng.Stop()

	// --- Data upstream: OnData → DataBus → rules → Publish ---
	upstreamDP := core.DataPoint{
		Driver:    "upstream-plc",
		Tag:       "temperature",
		Value:     80.0,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}
	payload, _ := json.Marshal(upstreamDP)
	if err := st.FeedRaw(payload, "edge/plc/temperature"); err != nil {
		t.Fatalf("FeedRaw: %v", err)
	}

	// The driver's scheduler also produces data (tag "setpoint"), so we
	// may see multiple published points. Filter for the upstream one.
	var upstreamPublished *core.DataPoint
	published := st.DrainPublished(2 * time.Second)
	for i := range published {
		if published[i].Driver == "upstream-plc" && published[i].Tag == "temperature" {
			upstreamPublished = &published[i]
			break
		}
	}
	if upstreamPublished == nil {
		t.Fatalf("upstream: expected published point from upstream-plc/temperature, got %d points: %+v", len(published), published)
	}
	if upstreamPublished.Value != 80.0 {
		t.Errorf("upstream: expected value 80.0, got %v", upstreamPublished.Value)
	}

	// --- Command downstream: OnCommand → WriteTag → Driver.Write ---
	cmd := core.WriteCommand{
		Driver: "plc",
		Tag:    "setpoint",
		Value:  42.0,
		Type:   core.TypeFloat32,
	}
	st.FeedCommand(cmd)

	// Wait for the write to be executed.
	deadline := time.After(2 * time.Second)
	for {
		written := rd.GetWritten()
		if len(written) >= 1 {
			if written[0].Tag != "setpoint" {
				t.Errorf("downstream: expected tag setpoint, got %s", written[0].Tag)
			}
			if v, ok := written[0].Value.(float64); !ok || v != 42.0 {
				t.Errorf("downstream: expected value 42.0, got %v", written[0].Value)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout: write command not executed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
