package engine

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine/statistic"
	"github.com/CoreC-Dev/CoreC/rule"
)

// ============================================================
// 全链路规模负载测试：N 设备 × M 采集点
// ============================================================

// scaleMockDriver 是面向规模测试的高性能 mock 驱动。
// Read 直接返回预分配的 TagValue 切片，零 I/O 延迟，
// 从而测量的是核心本身的调度/总线/规则/缓存/发布开销。
type scaleMockDriver struct {
	name   string
	tags   []string
	values []core.TagValue
	status core.DriverStatus
}

func newScaleMockDriver(name string, tags []string) *scaleMockDriver {
	vals := make([]core.TagValue, len(tags))
	for i, t := range tags {
		vals[i] = core.TagValue{
			Tag:       t,
			Value:     42.5,
			Type:      core.TypeFloat64,
			Quality:   core.QualityGood,
			Timestamp: time.Now(),
		}
	}
	return &scaleMockDriver{
		name:   name,
		tags:   tags,
		values: vals,
		status: core.DriverStatus{Name: name, Type: "scale-mock", State: core.StateConnected},
	}
}

func (d *scaleMockDriver) Init(ctx context.Context, config core.DriverConfig) error {
	d.name = config.Name
	d.status = core.DriverStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	return nil
}
func (d *scaleMockDriver) Start(ctx context.Context) error { return nil }
func (d *scaleMockDriver) Stop() error                     { return nil }
func (d *scaleMockDriver) Restart(ctx context.Context, config core.DriverConfig) error {
	return nil
}
func (d *scaleMockDriver) Read(ctx context.Context, tags []string) ([]core.TagValue, error) {
	// 返回预分配的值，更新时间戳
	now := time.Now()
	for i := range d.values {
		d.values[i].Timestamp = now
	}
	return d.values, nil
}
func (d *scaleMockDriver) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	results := make([]core.WriteResult, len(commands))
	for i := range results {
		results[i] = core.WriteResult{Success: true}
	}
	return results, nil
}
func (d *scaleMockDriver) Subscribe(ctx context.Context, tags []string) (<-chan core.DataPoint, error) {
	return nil, core.ErrSubscribeNotSupported
}
func (d *scaleMockDriver) Name() string              { return d.name }
func (d *scaleMockDriver) Type() string              { return "scale-mock" }
func (d *scaleMockDriver) Status() core.DriverStatus { return d.status }
func (d *scaleMockDriver) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{CanRead: true, CanWrite: true}
}

// scaleMockTransport 是面向规模测试的 mock 传输，计数发布量。
type scaleMockTransport struct {
	name      string
	published atomic.Int64
	status    core.TransportStatus
}

func (t *scaleMockTransport) Init(ctx context.Context, config core.TransportConfig) error {
	t.name = config.Name
	t.status = core.TransportStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}
	return nil
}
func (t *scaleMockTransport) Start(ctx context.Context) error { return nil }
func (t *scaleMockTransport) Stop() error                     { return nil }
func (t *scaleMockTransport) Publish(ctx context.Context, point core.DataPoint) error {
	t.published.Add(1)
	return nil
}
func (t *scaleMockTransport) PublishBatch(ctx context.Context, points []core.DataPoint) error {
	t.published.Add(int64(len(points)))
	return nil
}
func (t *scaleMockTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (t *scaleMockTransport) OnData() <-chan core.DataPoint       { return nil }
func (t *scaleMockTransport) Name() string                        { return t.name }
func (t *scaleMockTransport) Type() string                        { return "scale-mock" }
func (t *scaleMockTransport) Status() core.TransportStatus        { return t.status }

// runScaleTest 运行一次全链路规模测试，返回性能指标。
func runScaleTest(numDrivers, tagsPerDriver int, interval, duration time.Duration) scaleResult {
	driverType := fmt.Sprintf("scale-mock-%dx%d", numDrivers, tagsPerDriver)
	core.RegisterDriver(driverType, func(config core.DriverConfig) (core.Driver, error) {
		tags := make([]string, tagsPerDriver)
		for i := range tags {
			tags[i] = fmt.Sprintf("tag_%04d", i)
		}
		return newScaleMockDriver(config.Name, tags), nil
	})

	transportType := fmt.Sprintf("scale-transport-%dx%d", numDrivers, tagsPerDriver)
	var tr *scaleMockTransport
	core.RegisterTransport(transportType, func(config core.TransportConfig) (core.Transport, error) {
		tr = &scaleMockTransport{status: core.TransportStatus{Name: config.Name, Type: config.Type, State: core.StateConnected}}
		return tr, nil
	})

	// 构建配置：N 个驱动，每个 M 个采集点
	drivers := make([]core.DriverConfig, numDrivers)
	for i := 0; i < numDrivers; i++ {
		tags := make([]core.TagConfig, tagsPerDriver)
		for j := 0; j < tagsPerDriver; j++ {
			tags[j] = core.TagConfig{
				Name:     fmt.Sprintf("tag_%04d", j),
				Address:  fmt.Sprintf("400%03d", j+1),
				Type:     "float64",
				Interval: interval.String(),
			}
		}
		drivers[i] = core.DriverConfig{
			Name:     fmt.Sprintf("dev_%04d", i),
			Type:     driverType,
			Settings: map[string]any{},
			Tags:     tags,
		}
	}

	cfg := &core.Config{
		Drivers: drivers,
		Transports: []core.TransportConfig{
			{Name: "sink", Type: transportType},
		},
		Rules: []core.RuleConfig{
			{Name: "forward-all", Match: "ALL", Action: "forward", Target: "sink"},
		},
	}

	eng := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		panic(fmt.Sprintf("engine start failed: %v", err))
	}

	// 运行指定时长
	time.Sleep(duration)

	stats := eng.Stats()
	// Capture push drops, subscriber drops, and in-flight before Stop
	ce := eng.(*CoreCEngine)
	pushDropped := uint64(ce.dataBus.PushDropped())
	subDropped := uint64(ce.dataBus.Dropped())
	inFlight := uint64(len(ce.dataBus.Channel()))

	eng.Stop()

	totalTags := numDrivers * tagsPerDriver
	elapsed := stats.Uptime.Seconds()
	pointsPerSec := float64(0)
	if elapsed > 0 {
		pointsPerSec = float64(stats.TotalRead) / elapsed
	}
	publishedPerSec := float64(0)
	if elapsed > 0 {
		publishedPerSec = float64(tr.published.Load()) / elapsed
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return scaleResult{
		numDrivers:      numDrivers,
		tagsPerDriver:   tagsPerDriver,
		totalTags:       totalTags,
		interval:        interval,
		duration:        duration,
		totalRead:       stats.TotalRead,
		totalPublish:    stats.TotalPublish,
		totalDropped:    subDropped,
		pointsPerSec:    pointsPerSec,
		publishedPerSec: publishedPerSec,
		allocMB:         float64(m.Alloc) / 1024 / 1024,
		goroutines:      runtime.NumGoroutine(),
		pushDropped:     pushDropped,
		inFlight:        inFlight,
	}
}

type scaleResult struct {
	numDrivers      int
	tagsPerDriver   int
	totalTags       int
	interval        time.Duration
	duration        time.Duration
	totalRead       uint64
	totalPublish    uint64
	totalDropped    uint64
	pointsPerSec    float64
	publishedPerSec float64
	avgLatencyMs    float64
	overruns        uint64
	allocMB         float64
	goroutines      int
	pushDropped     uint64 // DataBus 主通道丢弃（最老数据被挤出）
	inFlight        uint64 // 停机时仍在通道缓冲区内的数据点
}

func (r scaleResult) String() string {
	pct := float64(0)
	if r.totalRead > 0 {
		pct = float64(r.totalPublish) / float64(r.totalRead) * 100
	}
	return fmt.Sprintf(
		"设备=%d 采集点/设备=%d 总采集点=%d 间隔=%v | "+
			"读取=%d 发布=%d (%.1f%%) | "+
			"总线丢弃=%d 缓冲残留=%d 订阅丢弃=%d | "+
			"读取吞吐=%.0f pts/s 发布吞吐=%.0f pts/s | "+
			"平均延迟=%.3fms 超限=%d | "+
			"内存=%.1fMB 协程=%d",
		r.numDrivers, r.tagsPerDriver, r.totalTags, r.interval,
		r.totalRead, r.totalPublish, pct,
		r.pushDropped, r.inFlight, r.totalDropped,
		r.pointsPerSec, r.publishedPerSec,
		r.avgLatencyMs, r.overruns,
		r.allocMB, r.goroutines,
	)
}

// BenchmarkScaleEngine is the full-pipeline scale load test entry point.
// Run: go test -run=BenchmarkScaleEngine -bench=BenchmarkScaleEngine -v -timeout 120s
//
// Converted to a benchmark so it does NOT run during `go test ./...`
// (benchmarks are opt-in). 6 scenarios (max 500dev×200tag=100k data points),
// each running 3s, total 18s+ of pure sleep.
func BenchmarkScaleEngine(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping scale test in short mode")
	}

	scenarios := []struct {
		name    string
		drivers int
		tags    int
	}{
		{"10dev×100tag", 10, 100},
		{"50dev×100tag", 50, 100},
		{"100dev×100tag", 100, 100},
		{"100dev×500tag", 100, 500},
		{"200dev×500tag", 200, 500},
		{"500dev×200tag", 500, 200},
	}

	interval := 100 * time.Millisecond // 10 Hz 采集频率
	duration := 3 * time.Second

	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			r := runScaleTest(sc.drivers, sc.tags, interval, duration)
			b.Logf("\n┌─────────────────────────────────────────────────\n│ %s\n└─────────────────────────────────────────────────", r.String())

			// 基本断言：应该有数据流转
			if r.totalRead == 0 {
				b.Error("no data was read")
			}
			if r.totalPublish == 0 {
				b.Error("no data was published")
			}
		})
	}
}

// ============================================================
// DataBus 微基准
// ============================================================

func BenchmarkDataBusPush(b *testing.B) {
	bus := NewDataBus(8192)
	defer bus.Close()
	dp := core.DataPoint{Driver: "d", Tag: "t", Value: 1.0, Timestamp: time.Now()}

	// 消费者排空 channel
	go func() {
		for range bus.Channel() {
		}
	}()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bus.Push(dp)
		}
	})
}

func BenchmarkDataBusBroadcast(b *testing.B) {
	bus := NewDataBus(8192)
	defer bus.Close()

	// 创建多个订阅者
	numSubs := 10
	unsubs := make([]func(), numSubs)
	for i := 0; i < numSubs; i++ {
		ch, un := bus.Subscribe("")
		unsubs[i] = un
		go func(c <-chan core.DataPoint) {
			for range c {
			}
		}(ch)
	}
	defer func() {
		for _, un := range unsubs {
			un()
		}
	}()

	dp := core.DataPoint{Driver: "d", Tag: "t", Value: 1.0, Timestamp: time.Now()}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Broadcast(dp)
	}
}

func BenchmarkDataBusBroadcastManySubs(b *testing.B) {
	for _, numSubs := range []int{1, 10, 50, 100} {
		b.Run(fmt.Sprintf("subs=%d", numSubs), func(b *testing.B) {
			bus := NewDataBus(8192)
			defer bus.Close()

			unsubs := make([]func(), numSubs)
			for i := 0; i < numSubs; i++ {
				ch, un := bus.Subscribe("")
				unsubs[i] = un
				go func(c <-chan core.DataPoint) {
					for range c {
					}
				}(ch)
			}
			defer func() {
				for _, un := range unsubs {
					un()
				}
			}()

			dp := core.DataPoint{Driver: "d", Tag: "t", Value: 1.0, Timestamp: time.Now()}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				bus.Broadcast(dp)
			}
		})
	}
}

// ============================================================
// LatestCache 微基准
// ============================================================

func BenchmarkCacheUpdate(b *testing.B) {
	cache := NewLatestCache()
	dp := core.DataPoint{Driver: "dev_0000", Tag: "tag_0000", Value: 42.5, Timestamp: time.Now()}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Update(dp)
	}
}

func BenchmarkCacheUpdateConcurrent(b *testing.B) {
	cache := NewLatestCache()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			dp := core.DataPoint{
				Driver:    fmt.Sprintf("dev_%04d", i%100),
				Tag:       fmt.Sprintf("tag_%04d", i%500),
				Value:     42.5,
				Timestamp: time.Now(),
			}
			cache.Update(dp)
			i++
		}
	})
}

func BenchmarkCacheGetByDriver(b *testing.B) {
	cache := NewLatestCache()
	// 预填充：1 个驱动 500 个采集点
	for i := 0; i < 500; i++ {
		cache.Update(core.DataPoint{
			Driver:    "dev_0000",
			Tag:       fmt.Sprintf("tag_%04d", i),
			Value:     42.5,
			Timestamp: time.Now(),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.GetByDriver("dev_0000")
	}
}

// ============================================================
// 规则匹配微基准
// ============================================================

func BenchmarkRuleMatchAll(b *testing.B) {
	eng := rule.NewEngine()
	eng.SetRules([]core.RuleConfig{
		{Name: "catch-all", Match: "ALL", Action: "forward", Target: "sink"},
	})

	dp := core.DataPoint{Driver: "d", Tag: "t", Value: 42.5, Timestamp: time.Now()}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.Match(dp)
	}
}

func BenchmarkRuleMatchMany(b *testing.B) {
	for _, numRules := range []int{1, 10, 50, 100} {
		b.Run(fmt.Sprintf("rules=%d", numRules), func(b *testing.B) {
			eng := rule.NewEngine()
			rules := make([]core.RuleConfig, numRules)
			for i := 0; i < numRules-1; i++ {
				rules[i] = core.RuleConfig{
					Name:     fmt.Sprintf("rule-%d", i),
					Match:    fmt.Sprintf("tag == 'nonexistent_%d'", i),
					Action:   "forward",
					Target:   "sink",
					Priority: i,
				}
			}
			// 最后一条是 catch-all
			rules[numRules-1] = core.RuleConfig{
				Name: "catch-all", Match: "ALL", Action: "forward", Target: "sink", Priority: 999,
			}
			eng.SetRules(rules)

			dp := core.DataPoint{Driver: "d", Tag: "t", Value: 42.5, Timestamp: time.Now()}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				eng.Match(dp)
			}
		})
	}
}

// ============================================================
// 处理流水线微基准（cache + broadcast + rule + publish）
// ============================================================

func BenchmarkProcessingPipeline(b *testing.B) {
	for _, numDrivers := range []int{10, 100, 500} {
		for _, tagsPerDriver := range []int{100, 500} {
			label := fmt.Sprintf("dev=%d/tags=%d", numDrivers, tagsPerDriver)
			b.Run(label, func(b *testing.B) {
				// 构建引擎内部组件
				bus := NewDataBus(8192)
				defer bus.Close()
				cache := NewLatestCache()
				ruleEng := rule.NewEngine()
				ruleEng.SetRules([]core.RuleConfig{
					{Name: "all", Match: "ALL", Action: "forward", Target: "sink"},
				})

				// mock transport 计数
				var published atomic.Int64
				publish := func(point core.DataPoint) {
					published.Add(1)
				}

				// 预生成数据点
				points := make([]core.DataPoint, numDrivers*tagsPerDriver)
				idx := 0
				for d := 0; d < numDrivers; d++ {
					for t := 0; t < tagsPerDriver; t++ {
						points[idx] = core.DataPoint{
							Driver:    fmt.Sprintf("dev_%04d", d),
							Tag:       fmt.Sprintf("tag_%04d", t),
							Value:     42.5,
							Type:      core.TypeFloat64,
							Timestamp: time.Now(),
						}
						idx++
					}
				}

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					p := points[i%len(points)]
					cache.Update(p)
					bus.Broadcast(p)
					result := ruleEng.Match(p)
					if result != nil {
						publish(p)
					}
				}
			})
		}
	}
}

// ============================================================
// Scheduler 调度吞吐微基准
// ============================================================

func BenchmarkSchedulerManyTasks(b *testing.B) {
	for _, numTasks := range []int{10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("tasks=%d", numTasks), func(b *testing.B) {
			var totalRead atomic.Int64
			sched := NewScheduler(
				func(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
					vals := make([]core.TagValue, len(tags))
					now := time.Now()
					for i := range vals {
						vals[i] = core.TagValue{Tag: tags[i], Value: 42.5, Timestamp: now}
					}
					return vals, nil
				},
				func(driver string, values []core.TagValue, _ int) {
					totalRead.Add(int64(len(values)))
				},
				0,                      // default error-throttle window
				statistic.NewManager(), // isolated stat manager
			)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sched.Start(ctx)
			defer sched.Stop()

			// 添加任务
			for i := 0; i < numTasks; i++ {
				tags := make([]string, 10)
				for j := range tags {
					tags[j] = fmt.Sprintf("tag_%d", j)
				}
				sched.AddTask(core.ScheduleTask{
					ID:       fmt.Sprintf("task-%d", i),
					Driver:   fmt.Sprintf("dev-%d", i),
					Tags:     tags,
					Interval: 10 * time.Millisecond,
				})
			}

			// 运行 200ms 测量吞吐
			b.ResetTimer()
			start := totalRead.Load()
			time.Sleep(200 * time.Millisecond)
			b.StopTimer()

			read := totalRead.Load() - start
			b.ReportMetric(float64(read)/0.2, "pts/s")
		})
	}
}

// 防止 unused import 警告
var _ = sync.Once{}
