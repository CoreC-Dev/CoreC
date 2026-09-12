package engine

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/transport/parser"
)

// ============================================================
// Performance benchmarks for the chained core.
// Measures end-to-end throughput and latency from OnData to Publish.
// ============================================================

// benchTransport is a high-throughput mock transport for benchmarking.
// It uses atomic counters for publish tracking, eliminating channel
// overhead from the measurement.
type benchTransport struct {
	name     string
	status   core.TransportStatus
	dataCh   chan core.DataPoint
	cmdCh    chan core.WriteCommand
	pubCount atomic.Int64
}

func newBenchTransport(name string, bufSize int) *benchTransport {
	return &benchTransport{
		name:   name,
		dataCh: make(chan core.DataPoint, bufSize),
		cmdCh:  make(chan core.WriteCommand, bufSize),
	}
}

func (t *benchTransport) Init(ctx context.Context, config core.TransportConfig) error {
	t.name = config.Name
	t.status = core.TransportStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	return nil
}
func (t *benchTransport) Start(ctx context.Context) error { return nil }
func (t *benchTransport) Stop() error                     { return nil }
func (t *benchTransport) Publish(ctx context.Context, point core.DataPoint) error {
	t.pubCount.Add(1)
	return nil
}
func (t *benchTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	t.pubCount.Add(int64(len(points)))
	return nil
}
func (t *benchTransport) OnCommand() <-chan core.WriteCommand { return t.cmdCh }
func (t *benchTransport) OnData() <-chan core.DataPoint       { return t.dataCh }
func (t *benchTransport) Name() string                        { return t.name }
func (t *benchTransport) Type() string                        { return "bench" }
func (t *benchTransport) Status() core.TransportStatus        { return t.status }

// runtimeYield yields to let processing goroutines run.
func runtimeYield() {
	for i := 0; i < 3; i++ {
		select {
		case <-time.After(0):
		default:
		}
	}
}

// waitPublishedSettled waits until pubCount stops increasing for settleWindow,
// indicating the processing pipeline has drained. Returns the final count.
func waitPublishedSettled(bt *benchTransport, timeout, settleWindow time.Duration) int64 {
	deadline := time.Now().Add(timeout)
	last := bt.pubCount.Load()
	lastChange := time.Now()
	for {
		now := time.Now()
		if now.After(deadline) {
			break
		}
		cur := bt.pubCount.Load()
		if cur != last {
			last = cur
			lastChange = now
		}
		if now.Sub(lastChange) >= settleWindow {
			break // settled
		}
		runtimeYield()
	}
	return bt.pubCount.Load()
}

// BenchmarkChainedCore_Throughput measures end-to-end throughput:
// OnData → DataBus → processingLoop → Publish.
func BenchmarkChainedCore_Throughput(b *testing.B) {
	const transportType = "bench-throughput"
	var bt *benchTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		bt = newBenchTransport(config.Name, 8192)
		return bt, nil
	})
	core.RegisterDriver("mock-d-bench1", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-bench1")},
		Transports: []core.TransportConfig{
			{Name: "bench", Type: transportType, Settings: map[string]any{}},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "bench"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := New()
	if err := eng.Start(ctx, cfg); err != nil {
		b.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	dp := core.DataPoint{Driver: "up", Tag: "t", Value: 42.5, Type: core.TypeFloat64, Quality: core.QualityGood}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dp.Timestamp = time.Now()
		bt.dataCh <- dp
	}
	pub := waitPublishedSettled(bt, 30*time.Second, 200*time.Millisecond)
	b.StopTimer()
	b.ReportMetric(float64(pub)/b.Elapsed().Seconds(), "pts/s")
	if dropped := int64(b.N) - pub; dropped > 0 {
		b.ReportMetric(float64(dropped)/float64(b.N)*100, "%dropped")
	}
}

// BenchmarkChainedCore_WithRuleMatching measures throughput with
// a non-trivial rule expression (tag + value comparison).
func BenchmarkChainedCore_WithRuleMatching(b *testing.B) {
	const transportType = "bench-rules"
	var bt *benchTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		bt = newBenchTransport(config.Name, 8192)
		return bt, nil
	})
	core.RegisterDriver("mock-d-bench2", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-bench2")},
		Transports: []core.TransportConfig{
			{Name: "bench", Type: transportType, Settings: map[string]any{}},
		},
		Rules: []core.RuleConfig{
			{Name: "alert-high", Match: "tag == 'temp' && value > 50", Action: "alert", Target: "bench", Priority: 1},
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "bench", Priority: 999},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := New()
	if err := eng.Start(ctx, cfg); err != nil {
		b.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	dp := core.DataPoint{Driver: "up", Tag: "temp", Value: 42.5, Type: core.TypeFloat64, Quality: core.QualityGood}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dp.Timestamp = time.Now()
		bt.dataCh <- dp
	}
	pub := waitPublishedSettled(bt, 30*time.Second, 200*time.Millisecond)
	b.StopTimer()
	b.ReportMetric(float64(pub)/b.Elapsed().Seconds(), "pts/s")
	if dropped := int64(b.N) - pub; dropped > 0 {
		b.ReportMetric(float64(dropped)/float64(b.N)*100, "%dropped")
	}
}

// BenchmarkChainedCore_MultipleSources measures throughput with
// 3 concurrent OnData sources feeding into the same DataBus.
func BenchmarkChainedCore_MultipleSources(b *testing.B) {
	const transportType = "bench-multi-src"
	var transports []*benchTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		bt := newBenchTransport(config.Name, 4096)
		transports = append(transports, bt)
		return bt, nil
	})
	core.RegisterDriver("mock-d-bench3", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-bench3")},
		Transports: []core.TransportConfig{
			{Name: "src-1", Type: transportType, Settings: map[string]any{}},
			{Name: "src-2", Type: transportType, Settings: map[string]any{}},
			{Name: "src-3", Type: transportType, Settings: map[string]any{}},
			{Name: "out", Type: transportType, Settings: map[string]any{}},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "out"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := New()
	if err := eng.Start(ctx, cfg); err != nil {
		b.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	src1, src2, src3, out := transports[0], transports[1], transports[2], transports[3]
	dp := core.DataPoint{Driver: "up", Tag: "t", Value: 42.5, Type: core.TypeFloat64, Quality: core.QualityGood}

	b.ResetTimer()
	perSource := b.N / 3
	remainder := b.N % 3
	done := make(chan struct{}, 3)
	feed := func(tr *benchTransport, count int) {
		for i := 0; i < count; i++ {
			dp.Timestamp = time.Now()
			tr.dataCh <- dp
		}
		done <- struct{}{}
	}
	go feed(src1, perSource+remainder)
	go feed(src2, perSource)
	go feed(src3, perSource)
	<-done
	<-done
	<-done

	waitPublishedSettled(out, 30*time.Second, 200*time.Millisecond)
	pub := out.pubCount.Load()
	b.StopTimer()
	b.ReportMetric(float64(pub)/b.Elapsed().Seconds(), "pts/s")
	if dropped := int64(b.N) - pub; dropped > 0 {
		b.ReportMetric(float64(dropped)/float64(b.N)*100, "%dropped")
	}
}

// BenchmarkChainedCore_Latency measures per-point end-to-end latency:
// time from pushing into OnData to seeing it published.
func BenchmarkChainedCore_Latency(b *testing.B) {
	const transportType = "bench-latency"
	var bt *benchTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		bt = newBenchTransport(config.Name, 1024)
		return bt, nil
	})
	core.RegisterDriver("mock-d-bench4", func(config core.DriverConfig) (core.Driver, error) {
		return &mockDriver{}, nil
	})

	cfg := &core.Config{
		Drivers: []core.DriverConfig{dummyDriverConfig("mock-d-bench4")},
		Transports: []core.TransportConfig{
			{Name: "bench", Type: transportType, Settings: map[string]any{}},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "bench"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := New()
	if err := eng.Start(ctx, cfg); err != nil {
		b.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	dp := core.DataPoint{Driver: "up", Tag: "t", Value: 42.5, Type: core.TypeFloat64, Quality: core.QualityGood}

	var totalLatency int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dp.Timestamp = time.Now()
		start := time.Now()
		bt.dataCh <- dp
		for bt.pubCount.Load() < int64(i+1) {
			runtimeYield()
		}
		totalLatency += int64(time.Since(start))
	}
	b.StopTimer()
	if b.N > 0 {
		avgLatency := time.Duration(totalLatency / int64(b.N))
		b.ReportMetric(float64(avgLatency.Microseconds()), "µs/point")
	}
}

// BenchmarkChainedCore_ParserOverhead compares default vs jsonpath
// parser overhead in the chained core path.
func BenchmarkChainedCore_ParserOverhead(b *testing.B) {
	dp := core.DataPoint{Driver: "plc", Tag: "temp", Value: 42.5, Type: core.TypeFloat64, Quality: core.QualityGood}
	corecPayload, _ := json.Marshal(dp)
	thirdPartyPayload := []byte(`{"dev_id":"sensor-01","temp":23.5,"humidity":60}`)

	// Pre-create parsers.
	defaultParser, _ := parser.New(map[string]any{
		"parser": map[string]any{"type": "default"},
	})
	jsonpathParser, _ := parser.New(map[string]any{
		"parser": map[string]any{
			"type":      "jsonpath",
			"driver":    "lora",
			"tag":       "{{ .payload.dev_id }}",
			"value":     "{{ .payload.temp }}",
			"data-type": "float32",
		},
	})

	b.Run("default", func(b *testing.B) {
		const transportType = "bench-pd"
		var bt *benchTransport
		core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
			bt = newBenchTransport(config.Name, 8192)
			return bt, nil
		})
		core.RegisterDriver("mock-d-bench5a", func(config core.DriverConfig) (core.Driver, error) {
			return &mockDriver{}, nil
		})
		cfg := &core.Config{
			Drivers:    []core.DriverConfig{dummyDriverConfig("mock-d-bench5a")},
			Transports: []core.TransportConfig{{Name: "bench", Type: transportType, Settings: map[string]any{}}},
			Rules:      []core.RuleConfig{{Name: "f", Match: "ALL", Action: "forward", Target: "bench"}},
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		eng := New()
		if err := eng.Start(ctx, cfg); err != nil {
			b.Fatalf("Start: %v", err)
		}
		defer eng.Stop()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			point, _ := defaultParser.Parse(corecPayload, "")
			point.Timestamp = time.Now()
			bt.dataCh <- point
		}
		pub := waitPublishedSettled(bt, 30*time.Second, 200*time.Millisecond)
		b.StopTimer()
		b.ReportMetric(float64(pub)/b.Elapsed().Seconds(), "pts/s")
		if dropped := int64(b.N) - pub; dropped > 0 {
			b.ReportMetric(float64(dropped)/float64(b.N)*100, "%dropped")
		}
	})

	b.Run("jsonpath", func(b *testing.B) {
		const transportType = "bench-pj"
		var bt *benchTransport
		core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
			bt = newBenchTransport(config.Name, 8192)
			return bt, nil
		})
		core.RegisterDriver("mock-d-bench5b", func(config core.DriverConfig) (core.Driver, error) {
			return &mockDriver{}, nil
		})
		cfg := &core.Config{
			Drivers:    []core.DriverConfig{dummyDriverConfig("mock-d-bench5b")},
			Transports: []core.TransportConfig{{Name: "bench", Type: transportType, Settings: map[string]any{}}},
			Rules:      []core.RuleConfig{{Name: "f", Match: "ALL", Action: "forward", Target: "bench"}},
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		eng := New()
		if err := eng.Start(ctx, cfg); err != nil {
			b.Fatalf("Start: %v", err)
		}
		defer eng.Stop()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			point, _ := jsonpathParser.Parse(thirdPartyPayload, "lora/sensor-01/up")
			point.Timestamp = time.Now()
			bt.dataCh <- point
		}
		pub := waitPublishedSettled(bt, 30*time.Second, 200*time.Millisecond)
		b.StopTimer()
		b.ReportMetric(float64(pub)/b.Elapsed().Seconds(), "pts/s")
		if dropped := int64(b.N) - pub; dropped > 0 {
			b.ReportMetric(float64(dropped)/float64(b.N)*100, "%dropped")
		}
	})
}
